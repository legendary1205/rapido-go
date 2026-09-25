package httpapi

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// Presence is exact "who is connected right now", pushed by the nodes through
// POST /api/internal/node-live (nodelive.go) and kept in Redis:
//
//	presence:users              ZSET  username -> unix ms of the last node-live listing them
//	presence:live               STRING "1", 20 s TTL, refreshed by every node-live
//	presence:node:<id>:ports    HASH  port -> open connections, replaced on every node-live
//	presence:node:<id>:total    STRING open connections on the node
//
// A user is online iff their score is within presenceOnlineWindow of now. While
// presence:live is absent no node is sending presence at all (an old node
// fleet, or every node down), and the panel answers from users.online_at with
// the legacy onlineWindow instead - the column node reports keep updating.
const (
	presenceUsersKey     = "presence:users"
	presenceLiveKey      = "presence:live"
	presenceOnlineWindow = 15 * time.Second
	presenceKeyTTL       = 20 * time.Second
	presenceRetention    = 6 * time.Hour
)

// onlineCountTTL is how long GET /api/system reuses a computed online count.
// The dashboard and every bot poll that endpoint, and the number cannot
// meaningfully change faster than a node-live arrives.
const onlineCountTTL = time.Second

func presenceNodePortsKey(nodeID int32) string {
	return "presence:node:" + strconv.Itoa(int(nodeID)) + ":ports"
}

func presenceNodeTotalKey(nodeID int32) string {
	return "presence:node:" + strconv.Itoa(int(nodeID)) + ":total"
}

// presenceView is what Redis knew about a set of usernames at one instant.
type presenceView struct {
	// live is true when presence is authoritative (presence:live plus a live
	// report from every connected node); when false, online must be derived from
	// users.online_at instead.
	live bool
	// seen maps a username to the last time a node listed them as connected
	// (only users that have a presence entry).
	seen map[string]time.Time
}

// loadPresence reads presence:live and the presence score of every username in
// ONE pipelined round trip (a single ZMSCORE for the whole page, never one call
// per user). Any Redis trouble yields the zero view, i.e. "no presence": callers
// then fall back to the database and an unreachable Redis never fails a request.
func (h *Handler) loadPresence(ctx context.Context, usernames []string) presenceView {
	if len(usernames) == 0 {
		return presenceView{}
	}
	pipe := h.store.Cache.Raw().Pipeline()
	live := h.queuePresenceLive(ctx, pipe)
	scores := pipe.ZMScore(ctx, presenceUsersKey, usernames...)
	if _, err := pipe.Exec(ctx); err != nil {
		return presenceView{}
	}
	view := presenceView{live: live.authoritative()}
	for i, score := range scores.Val() {
		// A missing member comes back as 0, which no real score ever is.
		if score <= 0 || i >= len(usernames) {
			continue
		}
		if view.seen == nil {
			view.seen = make(map[string]time.Time)
		}
		view.seen[usernames[i]] = time.UnixMilli(int64(score)).UTC()
	}
	return view
}

// connectedNodesTTL is how long the list of connected nodes is reused while
// deciding whether presence can be trusted.
const connectedNodesTTL = 5 * time.Second

// connectedNodesCache remembers which nodes the panel currently marks
// connected. The zero value is ready to use.
type connectedNodesCache struct {
	mu  sync.Mutex
	ids []int32
	at  time.Time
}

// connectedNodeIDs are the ids of the nodes with status "connected", read from
// the database at most once per connectedNodesTTL. A read error keeps the last
// answer (or none), never failing a request over presence.
func (h *Handler) connectedNodeIDs(ctx context.Context) []int32 {
	c := &h.liveNodes
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < connectedNodesTTL {
		return c.ids
	}
	nodes, err := h.store.Queries.ListNodes(ctx)
	if err != nil {
		return c.ids
	}
	ids := make([]int32, 0, len(nodes))
	for _, n := range nodes {
		if n.Status == "connected" {
			ids = append(ids, n.ID)
		}
	}
	c.ids, c.at = ids, time.Now()
	return ids
}

// presenceLiveCheck is the queued answer to "can presence be trusted?".
type presenceLiveCheck struct {
	live   *redis.IntCmd
	totals []*redis.IntCmd
}

// queuePresenceLive queues, on pipe, the existence checks that decide whether
// presence is authoritative: presence:live plus the live total of EVERY node the
// panel marks connected. One node still on an old build (mid-rollout) or one
// whose live channel is down would otherwise show all of its users as offline
// while the rest of the fleet reports, so any missing node sends the caller
// back to the database definition.
func (h *Handler) queuePresenceLive(ctx context.Context, pipe redis.Pipeliner) presenceLiveCheck {
	check := presenceLiveCheck{live: pipe.Exists(ctx, presenceLiveKey)}
	for _, id := range h.connectedNodeIDs(ctx) {
		check.totals = append(check.totals, pipe.Exists(ctx, presenceNodeTotalKey(id)))
	}
	return check
}

// authoritative is valid only after the pipeline it was queued on has run.
func (c presenceLiveCheck) authoritative() bool {
	if c.live.Val() == 0 {
		return false
	}
	for _, t := range c.totals {
		if t.Val() == 0 {
			return false
		}
	}
	return true
}

// userStatus is the `online` flag and `online_at` of one user: online_at is
// the later of the database value and the presence score, and online is the
// presence definition (seen within 15 s) while presence is live, the legacy
// definition (users.online_at within 180 s) while it is not.
func (v presenceView) userStatus(username string, dbOnlineAt pgtype.Timestamptz, now time.Time) (online bool, onlineAt *time.Time) {
	onlineAt = timestamptzToPtr(dbOnlineAt)
	seenAt, seen := v.seen[username]
	if seen && (onlineAt == nil || seenAt.After(*onlineAt)) {
		s := seenAt
		onlineAt = &s
	}
	if v.live {
		return seen && !seenAt.Before(now.Add(-presenceOnlineWindow)), onlineAt
	}
	return dbOnlineAt.Valid && !dbOnlineAt.Time.Before(now.Add(-onlineWindow)), onlineAt
}

// onlineCountCache holds the last online-user count per scope for
// onlineCountTTL. Scope 0 is the whole fleet (sudo); any other key is an admin
// id. The zero value is ready to use.
type onlineCountCache struct {
	mu      sync.Mutex
	entries map[int32]onlineCountEntry
}

type onlineCountEntry struct {
	count int64
	at    time.Time
}

func (c *onlineCountCache) get(scope int32, now time.Time) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[scope]
	if !ok || now.Sub(e.at) >= onlineCountTTL || now.Before(e.at) {
		return 0, false
	}
	return e.count, true
}

func (c *onlineCountCache) put(scope int32, count int64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int32]onlineCountEntry)
	}
	c.entries[scope] = onlineCountEntry{count: count, at: now}
}

// clear drops every cached count (tests use it to observe fresh state).
func (c *onlineCountCache) clear() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// onlineUsersCount is GET /api/system's online_users. scope is the reseller's
// admin id, or invalid for the whole fleet.
//
// With presence live it is exact: ZCOUNT of the presence set for the fleet; for
// a reseller, the online usernames are fetched from Redis and counted against
// that admin with one indexed query. Without presence it is the legacy
// database count. Either way the number is cached for onlineCountTTL.
func (h *Handler) onlineUsersCount(ctx context.Context, scope pgtype.Int4) (int64, error) {
	var scopeKey int32
	if scope.Valid {
		scopeKey = scope.Int32
	}
	now := time.Now()
	if n, ok := h.onlineCache.get(scopeKey, now); ok {
		return n, nil
	}

	n, ok, err := h.presenceOnlineCount(ctx, scope, now)
	if err != nil {
		return 0, err
	}
	if !ok {
		n, err = h.store.Queries.CountOnlineUsersSince(ctx, generated.CountOnlineUsersSinceParams{
			Cutoff: timestamptzFromTime(now.UTC().Add(-onlineWindow)), AdminID: scope,
		})
		if err != nil {
			return 0, err
		}
	}
	h.onlineCache.put(scopeKey, n, time.Now())
	return n, nil
}

// presenceOnlineCount counts online users from Redis. ok is false when presence
// is not live (or Redis cannot be read), telling the caller to use the database.
// err is only ever a database error from the reseller-scoped count.
func (h *Handler) presenceOnlineCount(ctx context.Context, scope pgtype.Int4, now time.Time) (count int64, ok bool, err error) {
	cutoff := strconv.FormatInt(now.Add(-presenceOnlineWindow).UnixMilli(), 10)
	pipe := h.store.Cache.Raw().Pipeline()
	live := h.queuePresenceLive(ctx, pipe)
	if !scope.Valid {
		zcount := pipe.ZCount(ctx, presenceUsersKey, cutoff, "+inf")
		if _, execErr := pipe.Exec(ctx); execErr != nil || !live.authoritative() {
			return 0, false, nil
		}
		return zcount.Val(), true, nil
	}

	names := pipe.ZRangeByScore(ctx, presenceUsersKey, &redis.ZRangeBy{Min: cutoff, Max: "+inf"})
	if _, execErr := pipe.Exec(ctx); execErr != nil || !live.authoritative() {
		return 0, false, nil
	}
	if len(names.Val()) == 0 {
		return 0, true, nil
	}
	count, err = h.store.Queries.CountAdminUsersByUsernames(ctx, generated.CountAdminUsersByUsernamesParams{
		Usernames: names.Val(), AdminID: scope.Int32,
	})
	return count, true, err
}

// RunPresenceTrim drops presence entries that have not been refreshed for
// presenceRetention, once per interval. A username stays in presence:users
// after it disconnects (the score is simply left to age), so without this the
// set would grow by every user who ever connected. Meant to run inside the
// backend singleton next to the other periodic jobs.
func RunPresenceTrim(ctx context.Context, c *cache.Client, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := "(" + strconv.FormatInt(time.Now().Add(-presenceRetention).UnixMilli(), 10)
			// A shutdown in flight cancels the call; that is not worth a line.
			if err := c.Raw().ZRemRangeByScore(ctx, presenceUsersKey, "-inf", cutoff).Err(); err != nil && ctx.Err() == nil {
				logger.Error("presence: trim", "error", err)
			}
		}
	}
}
