package telegrambot

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

const gibI = int64(1) << 30

func (e *testEnv) buttonData() string {
	var b strings.Builder
	for _, x := range e.buttons() {
		b.WriteString(x.Data + "|")
	}
	return b.String()
}

func TestSearchFindsByPartOfAUsername(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_one", nil)
	e.seedUser("alice_two", nil)
	e.seedUser("bob_one", nil)

	e.send(sudoUID, "ali")
	list := e.screen()
	mustContain(t, list, "Search results")
	mustContain(t, list, "2 user(s)")
	data := e.buttonData()
	mustContain(t, data, "u:alice_one")
	mustContain(t, data, "u:alice_two")
	mustNotContain(t, data, "bob_one")

	e.send(sudoUID, "one")
	data = e.buttonData()
	mustContain(t, data, "u:alice_one")
	mustContain(t, data, "u:bob_one")

	// A single hit, and an exact name, open the card straight away.
	e.send(sudoUID, "bob")
	mustContain(t, e.screen(), "<b>bob_one</b>")
	mustContain(t, e.screen(), "https://panel.test/sub/")
	e.send(sudoUID, "alice_one")
	mustContain(t, e.screen(), "<b>alice_one</b>")

	e.send(sudoUID, "zzz_nobody")
	mustContain(t, e.screen(), "No users found")

	// Typing a search opens the card; tapping a result opens it too.
	e.send(sudoUID, "ali")
	e.tap(sudoUID, "u:alice_two")
	mustContain(t, e.screen(), "<b>alice_two</b>")
}

func TestUserCardShowsUsageExpiryOwnerAndAllLinks(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedAdmin("reseller1", 2002, false)
	expire := time.Now().Add(10*24*time.Hour + time.Hour).Unix()
	e.seedUser("alice_a", map[string]any{"data_limit": 10 * gibI, "expire": expire, "note": "vip"})
	e.mustAPI("PUT", "/api/user/alice_a", e.sudoToken, map[string]any{"status": "active"})
	if _, err := e.pool.Exec(context.Background(), "UPDATE users SET used_traffic = $1, online_at = now() - interval '5 minutes' WHERE username = 'alice_a'", 5*gibI); err != nil {
		t.Fatal(err)
	}

	e.send(sudoUID, "alice_a")
	card := e.screen()
	mustContain(t, card, "Active")
	mustContain(t, card, "█████") // half a bar
	mustContain(t, card, "50%")
	mustContain(t, card, "5.00 GB / 10.0 GB")
	mustContain(t, card, "11 days left")
	mustContain(t, card, time.Unix(expire, 0).UTC().Format("2006-01-02"))
	mustContain(t, card, "5 min ago")
	mustContain(t, card, "panel (no admin)")
	mustContain(t, card, "vip")
	// Every address the API lists, each in a monospace block. The token inside a
	// link is minted per request (it embeds its creation time), so compare the
	// shape of what the card shows rather than one call's exact string.
	links := e.userJSON("alice_a")["subscription_urls"].([]any)
	if len(links) != 2 {
		t.Fatalf("API lists %d links, want 2", len(links))
	}
	shown := regexp.MustCompile(`<code>(https://[^<]+)</code>`).FindAllStringSubmatch(card, -1)
	if len(shown) != 2 {
		t.Fatalf("card shows %d links, want the 2 the API lists:\n%s", len(shown), card)
	}
	for i, prefix := range []string{"https://panel.test/sub/", "https://alt.test/sub/"} {
		if !strings.HasPrefix(shown[i][1], prefix) {
			t.Errorf("link %d = %q, want prefix %q", i, shown[i][1], prefix)
		}
		if strings.TrimPrefix(shown[i][1], prefix) != strings.TrimPrefix(shown[0][1], "https://panel.test/sub/") {
			t.Errorf("the two addresses carry different tokens: %q vs %q", shown[0][1], shown[i][1])
		}
	}

	// Never the proxies' own secrets.
	proxies := e.userJSON("alice_a")["proxies"].(map[string]any)
	uuid := proxies["vless"].(map[string]any)["id"].(string)
	mustNotContain(t, card, uuid)

	labels := ""
	for _, b := range e.buttons() {
		labels += b.Text + "|"
	}
	for _, want := range []string{"Disable", "Reset usage", "Extend", "Add data", "QR", "Note", "Revoke link", "Delete", "Back", "Home"} {
		mustContain(t, labels, want)
	}
}

func TestDisableThenEnable(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", nil)
	e.send(sudoUID, "alice_a")

	e.press(sudoUID, "Disable")
	if got := e.userJSON("alice_a")["status"]; got != "disabled" {
		t.Fatalf("status after Disable = %v", got)
	}
	mustContain(t, e.screen(), "Disabled")
	mustContain(t, e.screen(), "User disabled.")
	if !e.hasButtonPrefix("t:") {
		t.Error("card lost its toggle button")
	}
	labels := ""
	for _, b := range e.buttons() {
		labels += b.Text + "|"
	}
	mustContain(t, labels, "Enable")
	mustNotContain(t, labels, "Disable")

	e.press(sudoUID, "Enable")
	if got := e.userJSON("alice_a")["status"]; got != "active" {
		t.Fatalf("status after Enable = %v", got)
	}
	mustContain(t, e.screen(), "User enabled.")
}

func TestResetUsageAsksFirstAndCanBeCancelled(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", map[string]any{"data_limit": 10 * gibI})
	if _, err := e.pool.Exec(context.Background(), "UPDATE users SET used_traffic = $1 WHERE username = 'alice_a'", 5*gibI); err != nil {
		t.Fatal(err)
	}
	used := func() float64 { return num(e.userJSON("alice_a"), "used_traffic") }

	e.send(sudoUID, "alice_a")
	e.press(sudoUID, "Reset usage")
	mustContain(t, e.screen(), "Reset the data usage")
	if used() != float64(5*gibI) {
		t.Fatal("usage was reset before the confirmation")
	}
	e.press(sudoUID, "Cancel")
	mustContain(t, e.screen(), "<b>alice_a</b>")
	if used() != float64(5*gibI) {
		t.Fatal("cancelling still reset the usage")
	}

	e.press(sudoUID, "Reset usage")
	e.press(sudoUID, "Yes")
	if used() != 0 {
		t.Errorf("used_traffic after reset = %v, want 0", used())
	}
	mustContain(t, e.screen(), "Usage reset.")
}

func TestExtendExpiry(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	now := time.Now().Unix()
	e.seedUser("future_u", map[string]any{"expire": now + 10*86400, "next_plan": map[string]any{"data_limit": 7 * gibI, "expire": 86400, "add_remaining_traffic": false, "fire_on_either": true}})
	e.seedUser("stale_u", map[string]any{"expire": now + 100})
	e.seedUser("forever_u", nil)
	// An expired user renews from today, not from a date long past.
	if _, err := e.pool.Exec(context.Background(), "UPDATE users SET expire = $1, status = 'expired' WHERE username = 'stale_u'", now-5*86400); err != nil {
		t.Fatal(err)
	}
	expire := func(name string) int64 { return int64(num(e.userJSON(name), "expire")) }

	e.send(sudoUID, "future_u")
	e.press(sudoUID, "Extend")
	mustContain(t, e.screen(), "How many days")
	e.press(sudoUID, "+7")
	if got, want := expire("future_u"), now+10*86400+7*86400; got != want {
		t.Errorf("expire after +7 = %d, want %d", got, want)
	}
	mustContain(t, e.screen(), "Added 7 days")
	// A scheduled next plan survives an edit made from the console.
	if e.userJSON("future_u")["next_plan"] == nil {
		t.Error("extending through the console dropped the user's next plan")
	}

	// Custom amount, typed on a Persian keyboard.
	e.press(sudoUID, "Extend")
	e.press(sudoUID, "Custom")
	mustContain(t, e.screen(), "Send the number of days")
	e.send(sudoUID, "abc")
	mustContain(t, e.screen(), "not a valid number")
	e.send(sudoUID, "۳۰")
	if got, want := expire("future_u"), now+10*86400+7*86400+30*86400; got != want {
		t.Errorf("expire after custom 30 = %d, want %d", got, want)
	}
	mustContain(t, e.screen(), "<b>future_u</b>")

	e.send(sudoUID, "stale_u")
	e.press(sudoUID, "Extend")
	e.press(sudoUID, "+30")
	if got := expire("stale_u"); got < now+30*86400-5 || got > now+30*86400+60 {
		t.Errorf("expired user renewed to %d, want about now+30d (%d)", got, now+30*86400)
	}
	if got := e.userJSON("stale_u")["status"]; got != "active" {
		t.Errorf("renewed user status = %v, want active", got)
	}

	e.send(sudoUID, "forever_u")
	e.press(sudoUID, "Extend")
	e.press(sudoUID, "+7")
	mustContain(t, e.screen(), "no expiry date")
	if e.userJSON("forever_u")["expire"] != nil {
		t.Error("an unlimited user was given an expiry")
	}
}

func TestAddData(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", map[string]any{"data_limit": 10 * gibI})
	e.seedUser("free_u", nil)
	limit := func(name string) float64 { return num(e.userJSON(name), "data_limit") }

	e.send(sudoUID, "alice_a")
	e.press(sudoUID, "Add data")
	e.press(sudoUID, "+5 GB")
	if limit("alice_a") != float64(15*gibI) {
		t.Errorf("limit after +5 GB = %v", limit("alice_a"))
	}
	mustContain(t, e.screen(), "15.0 GB")

	e.press(sudoUID, "Add data")
	e.press(sudoUID, "Custom")
	e.send(sudoUID, "2.5")
	if limit("alice_a") != float64(15*gibI+gibI*5/2) {
		t.Errorf("limit after +2.5 GB = %v", limit("alice_a"))
	}

	e.send(sudoUID, "free_u")
	e.press(sudoUID, "Add data")
	e.press(sudoUID, "+10 GB")
	mustContain(t, e.screen(), "unlimited data")
	if e.userJSON("free_u")["data_limit"] != nil {
		t.Error("an unlimited user was given a data limit")
	}
}

func TestSetNoteAndHTMLIsEscaped(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", nil)
	e.send(sudoUID, "alice_a")

	e.press(sudoUID, "Note")
	mustContain(t, e.screen(), "Send the new note")
	nasty := `<script>alert(1)</script> & <b>bold</b>`
	e.send(sudoUID, nasty)
	if got := e.userJSON("alice_a")["note"]; got != nasty {
		t.Fatalf("stored note = %v, want the raw text", got)
	}
	card := e.screen()
	mustContain(t, card, "&lt;script&gt;alert(1)&lt;/script&gt; &amp; &lt;b&gt;bold&lt;/b&gt;")
	mustNotContain(t, card, "<script>")
	mustNotContain(t, card, "<b>bold</b>")

	// Too long: the panel counts bytes, so does the console, and nothing is saved.
	e.press(sudoUID, "Note")
	e.send(sudoUID, strings.Repeat("ی", 300)) // 600 bytes
	mustContain(t, e.screen(), "longer than 500 bytes")
	if e.userJSON("alice_a")["note"] != nasty {
		t.Error("an over-long note was saved")
	}
	e.send(sudoUID, "-")
	if e.userJSON("alice_a")["note"] != nil {
		t.Errorf("note after '-' = %v, want cleared", e.userJSON("alice_a")["note"])
	}

	// Anything echoed back that an admin typed is escaped, the search title included.
	e.send(sudoUID, "<i>x</i>")
	mustContain(t, e.screen(), "&lt;i&gt;x&lt;/i&gt;")
	mustNotContain(t, e.screen(), "<i>x")
}

func TestDeleteNeedsConfirmationAndCancelKeepsTheUser(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", nil)
	e.send(sudoUID, "alice_a")

	e.press(sudoUID, "Delete")
	mustContain(t, e.screen(), "Permanently delete")
	if e.userJSON("alice_a") == nil {
		t.Fatal("user deleted before confirmation")
	}
	e.press(sudoUID, "Cancel")
	mustContain(t, e.screen(), "<b>alice_a</b>")
	if e.userJSON("alice_a") == nil {
		t.Fatal("Cancel deleted the user")
	}

	e.press(sudoUID, "Delete")
	e.press(sudoUID, "Yes")
	if e.userJSON("alice_a") != nil {
		t.Fatal("user still exists after confirming the delete")
	}
	mustContain(t, e.screen(), "deleted")
	if !e.hasButtonData("h") {
		t.Error("the deleted screen has no way home")
	}

	// A stale button pointing at the deleted user reads "not found", not an error dump.
	e.tap(sudoUID, "u:alice_a")
	mustContain(t, e.screen(), "User not found")
}

func TestRevokeSubscriptionRotatesTheSecret(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", nil)
	idOf := func() string {
		return e.userJSON("alice_a")["proxies"].(map[string]any)["vless"].(map[string]any)["id"].(string)
	}
	before := idOf()

	e.send(sudoUID, "alice_a")
	e.press(sudoUID, "Revoke link")
	mustContain(t, e.screen(), "Revoke the subscription link")
	if idOf() != before {
		t.Fatal("secret rotated before confirmation")
	}
	e.press(sudoUID, "Yes")
	after := idOf()
	if after == before {
		t.Error("revoking did not rotate the proxy secret")
	}
	for _, c := range e.tg.snapshot() {
		if text, _ := c.Body["text"].(string); strings.Contains(text, before) || strings.Contains(text, after) {
			t.Errorf("a proxy uuid was sent to Telegram: %s", text)
		}
	}
	mustContain(t, e.screen(), "revoked")
}

func TestQRCodeIsSentAsAPhoto(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("alice_a", nil)
	e.send(sudoUID, "alice_a")
	e.press(sudoUID, "QR")

	var photo *fakeCall
	for _, c := range e.tg.snapshot() {
		if c.Method == "sendPhoto" {
			c := c
			photo = &c
		}
	}
	if photo == nil {
		t.Fatal("no photo was sent")
	}
	if !strings.HasPrefix(string(photo.File), "\x89PNG") {
		t.Errorf("photo is not a PNG: % x", photo.File[:min(8, len(photo.File))])
	}
	mustContain(t, fmt.Sprint(photo.Body["caption"]), "alice_a")
}

func TestUsersPaginateAndFilterByStatus(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	const total = 19 // three pages of 8
	for i := 0; i < total; i++ {
		e.seedUser(fmt.Sprintf("page_user_%02d", i), nil)
	}
	e.mustAPI("PUT", "/api/user/page_user_03", e.sudoToken, map[string]any{"status": "disabled"})
	e.mustAPI("PUT", "/api/user/page_user_07", e.sudoToken, map[string]any{"status": "disabled"})

	e.send(sudoUID, "/start")
	e.press(sudoUID, "Users")
	e.press(sudoUID, "All")

	seen := map[string]bool{}
	collect := func() int {
		n := 0
		for _, b := range e.buttons() {
			if strings.HasPrefix(b.Data, "u:") {
				name := strings.TrimPrefix(b.Data, "u:")
				if seen[name] {
					t.Errorf("%s appears on two pages", name)
				}
				seen[name] = true
				n++
			}
		}
		return n
	}
	label := func(want string) bool {
		for _, b := range e.buttons() {
			if b.Text == want {
				return true
			}
		}
		return false
	}

	if n := collect(); n != 8 {
		t.Fatalf("page 1 has %d users, want 8", n)
	}
	mustContain(t, e.screen(), "19 user(s) · page 1 of 3")
	if !label("1/3") || !label("›") || label("‹") {
		t.Errorf("page 1 navigation wrong: %v", e.buttons())
	}
	e.press(sudoUID, "›")
	if n := collect(); n != 8 {
		t.Fatalf("page 2 has %d users, want 8", n)
	}
	mustContain(t, e.screen(), "page 2 of 3")
	if !label("‹") || !label("›") {
		t.Errorf("page 2 should offer both directions: %v", e.buttons())
	}
	e.press(sudoUID, "›")
	if n := collect(); n != 3 {
		t.Fatalf("page 3 has %d users, want 3", n)
	}
	if label("›") || !label("‹") {
		t.Errorf("last page navigation wrong: %v", e.buttons())
	}
	if len(seen) != total {
		t.Errorf("saw %d distinct users across pages, want %d", len(seen), total)
	}
	e.press(sudoUID, "‹")
	mustContain(t, e.screen(), "page 2 of 3")

	// A page number past the end lands on the last page instead of an empty screen.
	e.tap(sudoUID, "l:*:99")
	mustContain(t, e.screen(), "page 3 of 3")

	// The status filter counts and lists only that status, and can be paged back from.
	e.press(sudoUID, "Back")
	e.press(sudoUID, "Disabled")
	mustContain(t, e.screen(), "2 user(s)")
	data := e.buttonData()
	mustContain(t, data, "u:page_user_03")
	mustContain(t, data, "u:page_user_07")
	if strings.Count(data, "u:") != 2 {
		t.Errorf("Disabled filter lists more than the two disabled users: %s", data)
	}
}

func TestSessionExpiresAndTheNextTextBecomesASearch(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.console.sessionTTL = 2 * time.Second

	e.send(sudoUID, "/start")
	e.press(sudoUID, "New user")
	mustContain(t, e.screen(), "Send the username")
	if _, ok := e.console.loadSession(context.Background(), sudoUID); !ok {
		t.Fatal("wizard did not store a session")
	}

	time.Sleep(2600 * time.Millisecond)
	if _, ok := e.console.loadSession(context.Background(), sudoUID); ok {
		t.Fatal("session outlived its TTL")
	}
	e.send(sudoUID, "late_user")
	mustContain(t, e.screen(), "No users found") // a search, not a wizard step
	if e.userJSON("late_user") != nil {
		t.Error("an expired wizard still created a user")
	}

	// A wizard that expires part-way through: the confirm tap is refused.
	e.console.sessionTTL = 2 * time.Second
	e.press(sudoUID, "Home")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "slow_user")
	e.press(sudoUID, "5")
	e.press(sudoUID, "30")
	mustContain(t, e.screen(), "Confirm the new user")
	time.Sleep(2600 * time.Millisecond)
	e.press(sudoUID, "Create user")
	mustContain(t, e.screen(), "That step expired")
	if e.userJSON("slow_user") != nil {
		t.Error("a user was created from an expired session")
	}
	if !e.hasButtonData("h") {
		t.Error("the expired screen has no way home")
	}
}

func TestWorstCaseCallbackDataStaysWithinTelegramsLimit(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	// 34 characters is the longest username the database holds.
	long := strings.Repeat("a", 32) + "-x"
	e.seedUser(long, nil)
	for i := 0; i < 10; i++ {
		e.seedUser(fmt.Sprintf("persian_note_%02d", i), map[string]any{"note": "یادداشت طولانی فارسی برای تست صفحه بندی"})
	}

	e.send(sudoUID, long) // the fake Bot API fails the test on any button over 64 bytes
	longest := 0
	for _, b := range e.buttons() {
		longest = max(longest, len(b.Data))
	}
	for _, label := range []string{"Extend", "Add data", "Reset usage", "Revoke link", "Delete", "Note"} {
		e.press(sudoUID, label)
		for _, b := range e.buttons() {
			longest = max(longest, len(b.Data))
		}
		e.tap(sudoUID, "u:"+long)
	}
	e.press(sudoUID, "Extend")
	e.press(sudoUID, "Custom")
	e.send(sudoUID, "/cancel")

	// A long Persian query rides in the pagination callbacks of a many-page result.
	e.send(sudoUID, "یادداشت طولانی فارسی برای تست صفحه بندی")
	mustContain(t, e.screen(), "page 1 of 2")
	for _, b := range e.buttons() {
		longest = max(longest, len(b.Data))
		if b.Text == "›" {
			if !strings.HasPrefix(b.Data, "l:*:1:") {
				t.Errorf("pagination callback %q lost its query", b.Data)
			}
			e.tap(sudoUID, b.Data)
			mustContain(t, e.screen(), "page 2 of 2")
			break
		}
	}
	if longest < 30 {
		t.Errorf("the worst-case check exercised no long callback data (longest %d bytes)", longest)
	}
}

func TestLanguageDefaultsToPersianAndCanBeToggled(t *testing.T) {
	e := newEnv(t)
	e.lang = "fa"
	e.send(sudoUID, "/start")
	mustContain(t, e.screen(), "کنسول مدیریت")
	mustNotContain(t, e.screen(), "Signed in as")

	e.press(sudoUID, "English")
	mustContain(t, e.screen(), "Signed in as")
	// The choice sticks even though the client still reports Persian.
	e.send(sudoUID, "/start")
	mustContain(t, e.screen(), "Signed in as")

	e.press(sudoUID, "فارسی")
	mustContain(t, e.screen(), "کنسول مدیریت")
}

func TestHelpAndUnknownCommands(t *testing.T) {
	e := newEnv(t)
	e.send(sudoUID, "/help")
	mustContain(t, e.screen(), "Help")
	mustContain(t, e.screen(), "Backup")
	if !e.hasButtonData("h") {
		t.Error("Help has no Home button")
	}
	e.send(sudoUID, "/whatever@somebot now")
	mustContain(t, e.screen(), "Signed in as")
}
