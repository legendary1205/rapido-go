package telegrambot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
)

func num(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

func TestUnknownChatGetsOneRefusalAndNothingElse(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", nil)

	e.send(strangerUID, "hello")
	if n := e.tg.count("sendMessage"); n != 1 {
		t.Fatalf("stranger got %d messages, want exactly 1", n)
	}
	mustContain(t, e.screen(), "not allowed")
	mustNotContain(t, e.screen(), "alice_a")

	// Everything after the first refusal is ignored: no reply, no lookup, no edit.
	e.send(strangerUID, "alice")
	e.send(strangerUID, "/start")
	e.tap(strangerUID, "u:alice_a")
	e.tap(strangerUID, "dl:alice_a:y")
	if e.tg.count("sendMessage") != 1 || e.tg.count("editMessageText") != 0 {
		t.Errorf("stranger was answered again: %d sends, %d edits", e.tg.count("sendMessage"), e.tg.count("editMessageText"))
	}
	if e.userJSON("alice_a") == nil {
		t.Error("a stranger's delete tap removed a user")
	}

	// Persian by default, English for other languages.
	e.tg.reset()
	e.sendLang(strangerUID+1, "hi", "fa")
	mustContain(t, e.screen(), "اجازه")
	e.tg.reset()
	e.sendLang(strangerUID+2, "hi", "de")
	mustContain(t, e.screen(), "not allowed")
}

func TestOnlyPrivateChatsAreServed(t *testing.T) {
	e := newEnv(t)
	// The listed account writing in a group must not open the console: a group id
	// on a notification list would otherwise hand the panel to every member.
	e.dispatch(tgUpdate{UpdateID: e.nextID(), Message: &tgMessage{
		MessageID: 1, From: &tgUser{ID: sudoUID}, Chat: tgChat{ID: -100123, Type: "supergroup"}, Text: "/start",
	}})
	// A listed *chat id* that is not the sender's own account is not enough either.
	e.dispatch(tgUpdate{UpdateID: e.nextID(), Message: &tgMessage{
		MessageID: 2, From: &tgUser{ID: strangerUID}, Chat: tgChat{ID: sudoUID, Type: "private"}, Text: "/start",
	}})
	if len(e.tg.snapshot()) != 0 {
		t.Errorf("console answered outside a private chat with the sender: %+v", e.tg.snapshot())
	}
}

func TestIgnoresEditedForwardedAndJunk(t *testing.T) {
	e := newEnv(t)
	msg := func(m tgMessage) {
		if m.From == nil {
			m.From = &tgUser{ID: sudoUID}
		}
		m.Chat = tgChat{ID: m.From.ID, Type: "private"}
		e.dispatch(tgUpdate{UpdateID: e.nextID(), Message: &m})
	}
	msg(tgMessage{MessageID: 1, Text: "/start", ForwardDate: 1700000000})
	msg(tgMessage{MessageID: 2, Text: "/start", ForwardOrigin: json.RawMessage(`{"type":"user"}`)})
	msg(tgMessage{MessageID: 3, Text: "/start", ViaBot: &tgUser{ID: 5, IsBot: true}})
	msg(tgMessage{MessageID: 4, Text: "/start", From: &tgUser{ID: sudoUID, IsBot: true}})
	msg(tgMessage{MessageID: 5, Text: ""})     // photo, sticker, ...
	e.dispatch(tgUpdate{UpdateID: e.nextID()}) // edited_message and the like decode to nothing
	if len(e.tg.snapshot()) != 0 {
		t.Fatalf("junk updates were answered: %+v", e.tg.snapshot())
	}

	e.send(sudoUID, strings.Repeat("x", 600))
	mustContain(t, e.screen(), "too long")
	e.tg.reset()
	e.send(sudoUID, "/start")
	mustContain(t, e.screen(), "Signed in as")
}

func TestDuplicateUpdateIsHandledOnce(t *testing.T) {
	e := newEnv(t)
	u := tgUpdate{UpdateID: 4242, Message: &tgMessage{
		MessageID: 1, From: &tgUser{ID: sudoUID}, Chat: tgChat{ID: sudoUID, Type: "private"}, Text: "/start",
	}}
	e.dispatch(u)
	e.dispatch(u) // Telegram redelivers the last batch after a restart
	if n := e.tg.count("sendMessage"); n != 1 {
		t.Errorf("a redelivered update was handled again: %d messages", n)
	}
}

func TestThrottleDropsAFloodButNotABurst(t *testing.T) {
	e := newEnv(t)
	e.console.limitBurst, e.console.limitPerSec = 3, 0.0001
	for i := 0; i < 6; i++ {
		e.send(sudoUID, "/start")
	}
	if n := e.tg.count("sendMessage"); n != 3 {
		t.Errorf("throttle let %d of 6 through, want the burst of 3", n)
	}
	e.tap(sudoUID, "sy")
	if e.tg.count("answerCallbackQuery") == 0 {
		t.Error("a throttled tap should still stop the button spinner")
	}
	if e.tg.count("editMessageText") != 0 {
		t.Error("a throttled tap must not run the action")
	}
}

func TestAuthorizationMapping(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const resellerUID, bossUID, bothUID = int64(2002), int64(4004), int64(5005)
	e.seedAdmin("reseller1", resellerUID, false)
	e.seedAdmin("boss", bossUID, true)
	e.seedAdmin("dual", bothUID, false)
	e.settings.set(func(v *integrationsettings.Values) { v.TelegramAdminIDs = []int64{sudoUID, bothUID} })

	cases := []struct {
		name     string
		uid      int64
		wantOK   bool
		wantName string
		wantSudo bool
	}{
		{"listed id acts as the env sudo account", sudoUID, true, envSudoUsername, true},
		{"admin bound to an account keeps their own privileges", resellerUID, true, "reseller1", false},
		{"a sudo admin bound to an account is sudo as themselves", bossUID, true, "boss", true},
		{"listed AND bound to a non-sudo admin is sudo", bothUID, true, envSudoUsername, true},
		{"unknown account is refused", strangerUID, false, "", false},
	}
	for _, c := range cases {
		got, ok := e.console.authorize(ctx, c.uid)
		if ok != c.wantOK || got.Username != c.wantName || got.IsSudo != c.wantSudo {
			t.Errorf("%s: authorize(%d) = %+v, %v; want %s sudo=%v ok=%v", c.name, c.uid, got, ok, c.wantName, c.wantSudo, c.wantOK)
		}
	}

	// Without an env sudo account a listed id falls back to a real sudo admin.
	e.console.d.SudoUsername = ""
	if got, ok := e.console.authorize(ctx, sudoUID); !ok || got.Username != "boss" || !got.IsSudo {
		t.Errorf("fallback sudo = %+v, %v; want the sudo admin boss", got, ok)
	}

	// Deleting the admin ends their access at once.
	e.mustAPI("DELETE", "/api/admin/reseller1", e.sudoToken, nil)
	if _, ok := e.console.authorize(ctx, resellerUID); ok {
		t.Error("a deleted admin can still use the console")
	}
}

func TestSudoSeesSystemUsersAndNodes(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", map[string]any{"data_limit": 10 << 30})
	e.seedUser("bob_bb", nil)
	e.seedUser("carol_c", nil)
	e.mustAPI("PUT", "/api/user/carol_c", e.sudoToken, map[string]any{"status": "disabled"})

	node := e.mustAPI("POST", "/api/node", e.sudoToken, map[string]any{"name": "node-fra", "address": "10.0.0.1"})
	nodeID := int32(num(node, "id"))
	payload, _ := json.Marshal(map[string]any{
		"collected_at": time.Now().UTC(), "uptime_seconds": 3600, "xray_running": true,
		"tunnels": []map[string]any{
			{"name": "wg0", "up": true, "present": true, "peers": []map[string]any{{"last_handshake_age_seconds": 12.0}}},
			{"name": "wg1", "up": false, "present": true},
		},
	})
	if _, err := e.queries.InsertHostMetric(context.Background(), generated.InsertHostMetricParams{
		NodeID: pgtype.Int4{Int32: nodeID, Valid: true}, CollectedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CpuPercent: pgtype.Float8{Float64: 12, Valid: true}, MemPercent: pgtype.Float8{Float64: 40, Valid: true},
		TunnelsUp: pgtype.Int4{Int32: 1, Valid: true}, TunnelsTotal: pgtype.Int4{Int32: 2, Valid: true},
		Healthy: true, Payload: pgtype.Text{String: string(payload), Valid: true},
	}); err != nil {
		t.Fatalf("InsertHostMetric: %v", err)
	}

	e.send(sudoUID, "/start")
	mustContain(t, e.screen(), "Signed in as")
	for _, data := range []string{"sy", "us", "nu", "nd", "bk", "hp", "lg"} {
		if !e.hasButtonData(data) {
			t.Errorf("sudo home is missing the %q button", data)
		}
	}

	e.press(sudoUID, "System")
	sys := e.screen()
	mustContain(t, sys, "Users: <b>3</b>")
	mustContain(t, sys, "active 2")
	mustContain(t, sys, "disabled 1")
	mustContain(t, sys, "Nodes:")
	mustContain(t, sys, "1 tunnel(s) with a problem")
	if !e.hasButtonData("h") {
		t.Error("System has no Home button")
	}

	e.press(sudoUID, "Home")
	e.press(sudoUID, "Nodes")
	nodes := e.screen()
	mustContain(t, nodes, "node-fra")
	mustContain(t, nodes, "10.0.0.1")
	mustContain(t, nodes, "wg0")
	mustContain(t, nodes, "wg1")
	mustContain(t, nodes, "down")
	mustContain(t, nodes, "CPU 12%")

	e.press(sudoUID, "Home")
	e.press(sudoUID, "Users")
	mustContain(t, e.screen(), "Users")
	labels := ""
	for _, b := range e.buttons() {
		labels += b.Text + "|"
	}
	mustContain(t, labels, "All (3)")
	mustContain(t, labels, "Disabled (1)")

	e.press(sudoUID, "All")
	listed := ""
	for _, b := range e.buttons() {
		listed += b.Data + "|"
	}
	for _, name := range []string{"alice_a", "bob_bb", "carol_c"} {
		mustContain(t, listed, "u:"+name)
	}
}

func TestNonSudoAdminOnlySeesAndTouchesOwnUsers(t *testing.T) {
	e := newEnv(t)
	const r1UID, r2UID = int64(2002), int64(3003)
	e.seedInbound()
	tok1 := e.seedAdmin("reseller1", r1UID, false)
	tok2 := e.seedAdmin("reseller2", r2UID, false)
	e.seedUserAs(tok1, "r1_user1", nil)
	e.seedUserAs(tok1, "r1_user2", nil)
	e.seedUserAs(tok2, "r2_user1", nil)
	e.seedUser("sudo_user", nil)

	e.send(r1UID, "/start")
	for _, data := range []string{"nd", "bk"} {
		if e.hasButtonData(data) {
			t.Errorf("a non-sudo admin is offered the %q button", data)
		}
	}
	for _, data := range []string{"sy", "us", "nu"} {
		if !e.hasButtonData(data) {
			t.Errorf("a non-sudo admin lacks the %q button", data)
		}
	}

	e.press(r1UID, "System")
	mustContain(t, e.screen(), "Users: <b>2</b>")
	mustContain(t, e.screen(), "your users only")
	mustNotContain(t, e.screen(), "Nodes:")

	e.press(r1UID, "Home")
	e.press(r1UID, "Users")
	e.press(r1UID, "All")
	data := ""
	for _, b := range e.buttons() {
		data += b.Data + "|"
	}
	mustContain(t, data, "u:r1_user1")
	mustContain(t, data, "u:r1_user2")
	for _, other := range []string{"r2_user1", "sudo_user"} {
		mustNotContain(t, data, other)
	}

	// Searching for someone else's user finds nothing, exactly like a user that
	// does not exist.
	e.send(r1UID, "r2_user1")
	mustContain(t, e.screen(), "No users found")
	mustNotContain(t, e.screen(), "r2_user1</b>")
	e.send(r1UID, "no_such_user")
	missing := e.screen()
	mustContain(t, missing, "No users found")

	// Forging callbacks for someone else's user reads as "not found" and changes nothing.
	for _, cb := range []string{"u:r2_user1", "t:r2_user1", "rs:r2_user1:y", "rv:r2_user1:y", "ex:r2_user1:7", "dt:r2_user1:5", "dl:r2_user1:y", "qr:r2_user1"} {
		e.tap(r1UID, cb)
		if cb != "qr:r2_user1" {
			mustContain(t, e.screen(), "User not found")
		}
	}
	if u := e.userJSON("r2_user1"); u == nil || u["status"] != "active" {
		t.Errorf("another admin's user was modified through the console: %v", u)
	}
	if e.tg.count("sendPhoto") != 0 {
		t.Error("a QR code of someone else's user was sent")
	}

	// Sudo-only screens refuse a non-sudo admin even with a forged callback.
	e.tap(r1UID, "nd")
	mustContain(t, e.screen(), "do not have access")
	e.tap(r1UID, "bk:y")
	mustContain(t, e.screen(), "do not have access")
	if e.tg.count("sendDocument") != 0 {
		t.Error("a non-sudo admin received a database backup")
	}

	// The reseller's own users work as normal.
	e.tap(r1UID, "u:r1_user1")
	mustContain(t, e.screen(), "r1_user1")
}

func TestNonSudoAdminCreatesOwnedUsers(t *testing.T) {
	e := newEnv(t)
	const r1UID = int64(2002)
	e.seedInbound()
	e.seedAdmin("reseller1", r1UID, false)

	e.send(r1UID, "/start")
	e.press(r1UID, "New user")
	e.send(r1UID, "reseller_made")
	e.press(r1UID, "10")
	e.press(r1UID, "30")
	e.press(r1UID, "Create user")

	u := e.userJSON("reseller_made")
	if u == nil {
		t.Fatal("wizard did not create the user")
	}
	admin, _ := u["admin"].(map[string]any)
	if admin["username"] != "reseller1" {
		t.Errorf("owner = %v, want reseller1 (a created user belongs to the admin who made it)", u["admin"])
	}
	mustContain(t, e.screen(), "reseller1")
}
