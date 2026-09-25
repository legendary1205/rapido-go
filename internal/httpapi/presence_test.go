package httpapi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
)

// presSetLive marks presence as live, the way a node-live does.
func presSetLive(t *testing.T, h *Handler) {
	t.Helper()
	if err := h.store.Cache.Raw().Set(context.Background(), presenceLiveKey, "1", presenceKeyTTL).Err(); err != nil {
		t.Fatalf("set presence:live: %v", err)
	}
}

// presSeen records that a node saw each user connected `ago` ago.
func presSeen(t *testing.T, h *Handler, ago time.Duration, usernames ...string) {
	t.Helper()
	score := float64(time.Now().Add(-ago).UnixMilli())
	zs := make([]redis.Z, len(usernames))
	for i, u := range usernames {
		zs[i] = redis.Z{Score: score, Member: u}
	}
	if err := h.store.Cache.Raw().ZAdd(context.Background(), presenceUsersKey, zs...).Err(); err != nil {
		t.Fatalf("zadd presence:users: %v", err)
	}
}

// presCreateUser creates a vmess user through the API and returns its username.
func presCreateUser(t *testing.T, router http.Handler, token, username string) {
	t.Helper()
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": username, "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("create user %s: %d %v", username, resp.Code, resp.Body)
	}
}

func presInbound(t *testing.T, router http.Handler, token string) {
	t.Helper()
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
}

func presGetUser(t *testing.T, router http.Handler, token, username string) map[string]interface{} {
	t.Helper()
	resp := doRequest(t, router, "GET", "/api/user/"+username, token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get user %s: %d %v", username, resp.Code, resp.Body)
	}
	return resp.Body
}

func presParseTime(t *testing.T, v interface{}) time.Time {
	t.Helper()
	s, _ := v.(string)
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse time %v: %v", v, err)
	}
	return parsed
}

func TestPresenceViewUserStatus(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	db := func(ago time.Duration) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: now.Add(-ago), Valid: true}
	}
	seen := func(live bool, ago time.Duration) presenceView {
		return presenceView{live: live, seen: map[string]time.Time{"u": now.Add(-ago)}}
	}

	tests := []struct {
		name       string
		view       presenceView
		dbOnlineAt pgtype.Timestamptz
		wantOnline bool
		wantAt     *time.Time // nil = null
	}{
		{"live, seen 5s ago", seen(true, 5*time.Second), pgtype.Timestamptz{}, true, ptrTime(now.Add(-5 * time.Second))},
		{"live, seen exactly 15s ago", seen(true, 15*time.Second), pgtype.Timestamptz{}, true, ptrTime(now.Add(-15 * time.Second))},
		{"live, seen 16s ago", seen(true, 16*time.Second), pgtype.Timestamptz{}, false, ptrTime(now.Add(-16 * time.Second))},
		{"live, fresh db but never in presence: presence wins", presenceView{live: true}, db(time.Second), false, ptrTime(now.Add(-time.Second))},
		{"live, db newer than the presence score", seen(true, 60*time.Second), db(2 * time.Second), false, ptrTime(now.Add(-2 * time.Second))},
		{"live, presence newer than db", seen(true, 3*time.Second), db(time.Hour), true, ptrTime(now.Add(-3 * time.Second))},
		{"not live, db within 180s", presenceView{}, db(170 * time.Second), true, ptrTime(now.Add(-170 * time.Second))},
		{"not live, db older than 180s", presenceView{}, db(190 * time.Second), false, ptrTime(now.Add(-190 * time.Second))},
		{"not live, never seen", presenceView{}, pgtype.Timestamptz{}, false, nil},
		{"not live, stale score is only last-seen info", seen(false, 60*time.Second), pgtype.Timestamptz{}, false, ptrTime(now.Add(-60 * time.Second))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			online, at := tt.view.userStatus("u", tt.dbOnlineAt, now)
			if online != tt.wantOnline {
				t.Errorf("online = %v, want %v", online, tt.wantOnline)
			}
			switch {
			case tt.wantAt == nil && at != nil:
				t.Errorf("online_at = %v, want null", at)
			case tt.wantAt != nil && (at == nil || !at.Equal(*tt.wantAt)):
				t.Errorf("online_at = %v, want %v", at, tt.wantAt)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestOnlineCountCacheExpiresAfterOneSecond(t *testing.T) {
	var c onlineCountCache
	now := time.Now()
	if _, ok := c.get(0, now); ok {
		t.Fatal("empty cache returned a value")
	}
	c.put(0, 7, now)
	c.put(5, 2, now)
	if n, ok := c.get(0, now.Add(900*time.Millisecond)); !ok || n != 7 {
		t.Errorf("get within the window = %d %v, want 7 true", n, ok)
	}
	if n, ok := c.get(5, now); !ok || n != 2 {
		t.Errorf("scopes must not share entries: got %d %v", n, ok)
	}
	if _, ok := c.get(0, now.Add(time.Second)); ok {
		t.Error("entry still served after the 1s TTL")
	}
	c.clear()
	if _, ok := c.get(5, now); ok {
		t.Error("clear left an entry behind")
	}
}

func TestPresenceUserOnlineFlagUsesPresenceWhenLive(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	presInbound(t, router, token)
	presCreateUser(t, router, token, "pres_live_user")

	body := presGetUser(t, router, token, "pres_live_user")
	if body["online"] != false {
		t.Errorf("online = %v for a user nobody has seen, want false", body["online"])
	}
	if body["online_at"] != nil {
		t.Errorf("online_at = %v, want null", body["online_at"])
	}

	presSetLive(t, h)
	presSeen(t, h, 5*time.Second, "pres_live_user")
	body = presGetUser(t, router, token, "pres_live_user")
	if body["online"] != true {
		t.Errorf("online = %v with a 5s-old presence score, want true", body["online"])
	}
	if at := presParseTime(t, body["online_at"]); time.Since(at) > 15*time.Second || time.Since(at) < 3*time.Second {
		t.Errorf("online_at = %v, want the presence time (about 5s ago)", at)
	}

	// Older than the 15s window: offline, but the last-seen time is kept.
	presSeen(t, h, 40*time.Second, "pres_live_user")
	body = presGetUser(t, router, token, "pres_live_user")
	if body["online"] != false {
		t.Errorf("online = %v with a 40s-old presence score, want false", body["online"])
	}
	if at := presParseTime(t, body["online_at"]); time.Since(at) < 30*time.Second {
		t.Errorf("online_at = %v, want the (old) presence time", at)
	}

	// A fresh DB online_at (traffic seen by node-report) does not make the user
	// online while presence is live and says they are gone - but online_at is the
	// later of the two.
	if _, err := testPool(t).Exec(context.Background(), `UPDATE users SET online_at = now() WHERE username = 'pres_live_user'`); err != nil {
		t.Fatalf("update online_at: %v", err)
	}
	body = presGetUser(t, router, token, "pres_live_user")
	if body["online"] != false {
		t.Errorf("online = %v, want false: presence is authoritative while live", body["online"])
	}
	if at := presParseTime(t, body["online_at"]); time.Since(at) > 10*time.Second {
		t.Errorf("online_at = %v, want the fresher database value", at)
	}
}

func TestPresenceUserOnlineFlagFallsBackToDatabaseWhenNotLive(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	presInbound(t, router, token)
	presCreateUser(t, router, token, "pres_legacy_user")
	pool := testPool(t)

	set := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	set(`UPDATE users SET online_at = now() - interval '60 seconds' WHERE username = 'pres_legacy_user'`)
	if body := presGetUser(t, router, token, "pres_legacy_user"); body["online"] != true {
		t.Errorf("online = %v for online_at 60s ago with no presence, want true (legacy 180s window)", body["online"])
	}
	set(`UPDATE users SET online_at = now() - interval '300 seconds' WHERE username = 'pres_legacy_user'`)
	if body := presGetUser(t, router, token, "pres_legacy_user"); body["online"] != false {
		t.Errorf("online = %v for online_at 300s ago with no presence, want false", body["online"])
	}

	// A stray presence score without presence:live must not decide anything -
	// but it still counts as last-seen information.
	presSeen(t, h, time.Second, "pres_legacy_user")
	body := presGetUser(t, router, token, "pres_legacy_user")
	if body["online"] != false {
		t.Errorf("online = %v, want the database verdict (false) while presence:live is absent", body["online"])
	}
	if at := presParseTime(t, body["online_at"]); time.Since(at) > 10*time.Second {
		t.Errorf("online_at = %v, want max(database, presence) = the fresh presence score", at)
	}
}

// cmdCounter counts Redis commands by name, pipelined or not.
type cmdCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *cmdCounter) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	c.counts[name]++
}

func (c *cmdCounter) get(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[name]
}

func (c *cmdCounter) DialHook(next redis.DialHook) redis.DialHook { return next }
func (c *cmdCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		c.add(cmd.Name())
		return next(ctx, cmd)
	}
}
func (c *cmdCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			c.add(cmd.Name())
		}
		return next(ctx, cmds)
	}
}

func TestPresenceUserListFlagsEveryUserWithOneZMScore(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	presInbound(t, router, token)
	for _, name := range []string{"pres_list_a", "pres_list_b", "pres_list_c"} {
		presCreateUser(t, router, token, name)
	}
	presSetLive(t, h)
	presSeen(t, h, 2*time.Second, "pres_list_a", "pres_list_c")
	presSeen(t, h, time.Minute, "pres_list_b")

	counter := &cmdCounter{}
	h.store.Cache.Raw().AddHook(counter)

	resp := doRequest(t, router, "GET", "/api/users", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("list users: %d %v", resp.Code, resp.Body)
	}
	online := map[string]bool{}
	for _, raw := range resp.Body["users"].([]interface{}) {
		u := raw.(map[string]interface{})
		flag, ok := u["online"].(bool)
		if !ok {
			t.Fatalf("user %v has no boolean online field: %v", u["username"], u)
		}
		online[u["username"].(string)] = flag
	}
	for name, want := range map[string]bool{"pres_list_a": true, "pres_list_b": false, "pres_list_c": true} {
		if online[name] != want {
			t.Errorf("online[%s] = %v, want %v", name, online[name], want)
		}
	}
	if n := counter.get("zmscore"); n != 1 {
		t.Errorf("a 3-user page issued %d ZMSCORE commands, want exactly 1", n)
	}
	if n := counter.get("zscore"); n != 0 {
		t.Errorf("the page issued %d per-user ZSCORE commands, want 0", n)
	}
}

func TestPresenceSystemOnlineUsersFromRedisForSudo(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	presSetLive(t, h)
	// Not even in the users table: the fleet count is a plain ZCOUNT.
	presSeen(t, h, 3*time.Second, "ghost_a", "ghost_b")
	presSeen(t, h, 30*time.Second, "ghost_stale")

	resp := doRequest(t, router, "GET", "/api/system", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("system: %d %v", resp.Code, resp.Body)
	}
	if got := resp.Body["online_users"].(float64); got != 2 {
		t.Errorf("online_users = %v, want 2 (the 30s-old entry is outside the 15s window)", got)
	}

	// A second call within a second is served from the cache, even though
	// presence changed...
	presSeen(t, h, time.Second, "ghost_c")
	resp = doRequest(t, router, "GET", "/api/system", token, nil)
	if got := resp.Body["online_users"].(float64); got != 2 {
		t.Errorf("online_users = %v right after a change, want the cached 2", got)
	}
	// ...and reflects it once the cache is dropped.
	h.onlineCache.clear()
	resp = doRequest(t, router, "GET", "/api/system", token, nil)
	if got := resp.Body["online_users"].(float64); got != 3 {
		t.Errorf("online_users = %v after the cache expired, want 3", got)
	}
}

func TestPresenceSystemOnlineUsersScopedToReseller(t *testing.T) {
	router, sudoToken, h := newTestRouterAndHandler(t)
	presInbound(t, router, sudoToken)
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "pres-owner-a", "password": "pw12345", "is_sudo": false})
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "pres-owner-b", "password": "pw12345", "is_sudo": false})
	tokenA := loginAs(t, router, "pres-owner-a", "pw12345")
	tokenB := loginAs(t, router, "pres-owner-b", "pw12345")
	for _, name := range []string{"pres_a_one", "pres_a_two", "pres_a_idle"} {
		presCreateUser(t, router, tokenA, name)
	}
	for _, name := range []string{"pres_b_one", "pres_b_two"} {
		presCreateUser(t, router, tokenB, name)
	}

	presSetLive(t, h)
	presSeen(t, h, 2*time.Second, "pres_a_one", "pres_a_two", "pres_b_one", "pres_b_two", "someone_unknown")
	presSeen(t, h, time.Minute, "pres_a_idle")

	count := func(token string) float64 {
		t.Helper()
		resp := doRequest(t, router, "GET", "/api/system", token, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("system: %d %v", resp.Code, resp.Body)
		}
		return resp.Body["online_users"].(float64)
	}
	if got := count(tokenA); got != 2 {
		t.Errorf("owner-a online_users = %v, want 2 (own users only)", got)
	}
	if got := count(tokenB); got != 2 {
		t.Errorf("owner-b online_users = %v, want 2 (own users only)", got)
	}
	if got := count(sudoToken); got != 5 {
		t.Errorf("sudo online_users = %v, want 5 (every fresh presence entry)", got)
	}
}

func TestPresenceSystemOnlineUsersFallsBackToDatabaseWhenNotLive(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	presInbound(t, router, token)
	presCreateUser(t, router, token, "pres_db_recent")
	presCreateUser(t, router, token, "pres_db_old")
	pool := testPool(t)
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET online_at = now() - interval '90 seconds' WHERE username = 'pres_db_recent'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET online_at = now() - interval '400 seconds' WHERE username = 'pres_db_old'`); err != nil {
		t.Fatal(err)
	}
	// Presence entries exist but presence:live does not: they must be ignored.
	presSeen(t, h, time.Second, "ghost_a", "ghost_b", "ghost_c")

	resp := doRequest(t, router, "GET", "/api/system", token, nil)
	if got := resp.Body["online_users"].(float64); got != 1 {
		t.Errorf("online_users = %v, want 1 (the database count within 180s)", got)
	}

	// Once nodes report, the same call switches to presence.
	presSetLive(t, h)
	h.onlineCache.clear()
	resp = doRequest(t, router, "GET", "/api/system", token, nil)
	if got := resp.Body["online_users"].(float64); got != 3 {
		t.Errorf("online_users = %v with presence live, want 3", got)
	}
}

func TestRunPresenceTrimRemovesOnlyStaleEntries(t *testing.T) {
	_, _, h := newTestRouterAndHandler(t)
	presSeen(t, h, 7*time.Hour, "trim_old")
	presSeen(t, h, 5*time.Hour, "trim_recent")
	presSeen(t, h, time.Second, "trim_now")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunPresenceTrim(ctx, h.store.Cache, h.logger, 20*time.Millisecond)
		close(done)
	}()
	// The job must be gone before the test's Redis client is closed.
	defer func() {
		cancel()
		<-done
	}()

	rdb := h.store.Cache.Raw()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rdb.ZScore(context.Background(), presenceUsersKey, "trim_old").Err() == redis.Nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := rdb.ZScore(context.Background(), presenceUsersKey, "trim_old").Err(); err != redis.Nil {
		t.Errorf("the 7h-old entry is still present (err=%v)", err)
	}
	for _, kept := range []string{"trim_recent", "trim_now"} {
		if err := rdb.ZScore(context.Background(), presenceUsersKey, kept).Err(); err != nil {
			t.Errorf("%s was trimmed or unreadable: %v", kept, err)
		}
	}
}

// A connected node that has not sent a live report (an old build mid-rollout,
// or its live channel is down) must keep presence from being trusted: its
// users would all read as offline while every other node reports.
func TestPresenceNeedsALiveReportFromEveryConnectedNode(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	ctx := context.Background()
	presInbound(t, router, token)
	presCreateUser(t, router, token, "pres_rollout")

	nodeID, _ := createTestNode(t, router, token, "rollout-node")
	if err := h.store.Queries.MarkNodeConnectedIfNotDisabled(ctx, nodeID); err != nil {
		t.Fatalf("mark node connected: %v", err)
	}
	h.liveNodes = connectedNodesCache{}

	presSetLive(t, h)
	presSeen(t, h, 2*time.Second, "pres_rollout")

	// Presence says online, the node has sent nothing: the database definition
	// (never seen by a report) applies, so offline.
	if got := presGetUser(t, router, token, "pres_rollout")["online"]; got != false {
		t.Errorf("online = %v with a connected node that sent no live report, want false (database fallback)", got)
	}
	h.onlineCache.clear()
	resp := doRequest(t, router, "GET", "/api/system", token, nil)
	if got := resp.Body["online_users"].(float64); got != 0 {
		t.Errorf("online_users = %v, want 0 (database fallback while a node is silent)", got)
	}

	// The node's live report arrives: presence is authoritative again.
	if err := h.store.Cache.Raw().Set(ctx, presenceNodeTotalKey(nodeID), "0", presenceKeyTTL).Err(); err != nil {
		t.Fatalf("set node total: %v", err)
	}
	if got := presGetUser(t, router, token, "pres_rollout")["online"]; got != true {
		t.Errorf("online = %v once every connected node reports, want true", got)
	}
	h.onlineCache.clear()
	resp = doRequest(t, router, "GET", "/api/system", token, nil)
	if got := resp.Body["online_users"].(float64); got != 1 {
		t.Errorf("online_users = %v once every connected node reports, want 1", got)
	}
}
