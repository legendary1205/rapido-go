package telegrambot

import (
	"context"
	"testing"
	"time"
)

func TestNewUserWizardWithoutTemplates(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.send(sudoUID, "/start")
	e.press(sudoUID, "New user")
	mustContain(t, e.screen(), "Send the username")

	// Bad names are refused with the rule, and the wizard waits for a better one.
	for _, bad := range []string{"a", "ab", "has space", "bad/name", "نام"} {
		e.send(sudoUID, bad)
		mustContain(t, e.screen(), "Invalid username")
	}
	if e.userJSON("has space") != nil {
		t.Fatal("an invalid name reached the panel")
	}

	e.send(sudoUID, "newbie_user")
	mustContain(t, e.screen(), "Pick the data limit")
	mustContain(t, e.screen(), "newbie_user")
	e.press(sudoUID, "5")
	mustContain(t, e.screen(), "Pick the duration")
	e.press(sudoUID, "30")
	summary := e.screen()
	mustContain(t, summary, "Confirm the new user")
	mustContain(t, summary, "newbie_user")
	mustContain(t, summary, "5.00 GB")
	mustContain(t, summary, "30 days")
	if e.userJSON("newbie_user") != nil {
		t.Fatal("user created before the confirmation")
	}

	before := time.Now().Unix()
	e.press(sudoUID, "Create user")

	u := e.userJSON("newbie_user")
	if u == nil {
		t.Fatal("the wizard did not create the user")
	}
	if num(u, "data_limit") != float64(5*gibI) {
		t.Errorf("data_limit = %v, want 5 GB", u["data_limit"])
	}
	if exp := int64(num(u, "expire")); exp < before+30*86400 || exp > time.Now().Unix()+30*86400 {
		t.Errorf("expire = %d, want 30 days from now", exp)
	}
	if u["status"] != "active" {
		t.Errorf("status = %v, want active", u["status"])
	}
	if _, ok := u["proxies"].(map[string]any)["vless"]; !ok {
		t.Errorf("proxies = %v, want a vless proxy", u["proxies"])
	}

	// The reply is the card with the subscription links, plus a QR code image.
	card := e.screen()
	mustContain(t, card, "<b>newbie_user</b>")
	mustContain(t, card, "User created.")
	mustContain(t, card, "https://panel.test/sub/")
	mustContain(t, card, "https://alt.test/sub/")
	if e.tg.count("sendPhoto") != 1 {
		t.Errorf("sent %d QR photos, want 1", e.tg.count("sendPhoto"))
	}
	if _, ok := e.console.loadSession(context.Background(), sudoUID); ok {
		t.Error("the wizard session outlived a completed creation")
	}
}

func TestNewUserWizardTypedValuesUnlimitedAndBackNavigation(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()

	// Typed numbers, on a Persian keyboard.
	e.send(sudoUID, "/start")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "typed_user")
	e.send(sudoUID, "many")
	mustContain(t, e.screen(), "not a valid number")
	e.send(sudoUID, "۲٫۵")
	mustContain(t, e.screen(), "Pick the duration")
	e.send(sudoUID, "-4")
	mustContain(t, e.screen(), "not a valid number")
	e.send(sudoUID, "۷")
	e.press(sudoUID, "Create user")
	u := e.userJSON("typed_user")
	if u == nil {
		t.Fatal("typed_user was not created")
	}
	if num(u, "data_limit") != float64(gibI*5/2) {
		t.Errorf("data_limit = %v, want 2.5 GB", u["data_limit"])
	}
	if exp, want := int64(num(u, "expire")), time.Now().Unix()+7*86400; exp < want-30 || exp > want+30 {
		t.Errorf("expire = %d, want about %d", exp, want)
	}

	// Unlimited data and duration create a user with neither.
	e.press(sudoUID, "Home")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "forever_user")
	e.press(sudoUID, "∞")
	e.press(sudoUID, "∞")
	mustContain(t, e.screen(), "unlimited")
	e.press(sudoUID, "Create user")
	f := e.userJSON("forever_user")
	if f == nil || f["data_limit"] != nil || f["expire"] != nil {
		t.Errorf("forever_user = %v, want no limit and no expiry", f)
	}

	// Back steps through the wizard without losing what was entered.
	e.press(sudoUID, "Home")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "back_user")
	e.press(sudoUID, "10")
	mustContain(t, e.screen(), "Pick the duration")
	e.press(sudoUID, "Back")
	mustContain(t, e.screen(), "Pick the data limit")
	mustContain(t, e.screen(), "back_user")
	e.press(sudoUID, "Back")
	mustContain(t, e.screen(), "Send the username")
	e.press(sudoUID, "Home")
	mustContain(t, e.screen(), "Signed in as")
	if _, ok := e.console.loadSession(context.Background(), sudoUID); ok {
		t.Error("going Home left a wizard session behind")
	}
	if e.userJSON("back_user") != nil {
		t.Error("abandoning the wizard created a user")
	}
}

func TestNewUserWizardRejectsADuplicateAndLetsYouRename(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.seedUser("taken_name", nil)

	e.send(sudoUID, "/start")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "taken_name")
	e.press(sudoUID, "5")
	e.press(sudoUID, "30")
	e.press(sudoUID, "Create user")
	mustContain(t, e.screen(), "already exists") // the panel's own message, surfaced
	mustContain(t, e.screen(), "❌")

	e.press(sudoUID, "Change username")
	mustContain(t, e.screen(), "Send the username")
	e.send(sudoUID, "fresh_name")
	e.press(sudoUID, "5")
	e.press(sudoUID, "30")
	e.press(sudoUID, "Create user")
	if e.userJSON("fresh_name") == nil {
		t.Fatal("renaming after a duplicate did not create the user")
	}
}

func TestNewUserWizardWithATemplate(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.mustAPI("POST", "/api/user_template", e.sudoToken, map[string]any{
		"name": "Gold", "data_limit": 20 * gibI, "expire_duration": 60 * 86400,
		"username_prefix": "gold_", "inbounds": map[string][]string{"vless": {"VLESS TCP"}},
	})

	e.send(sudoUID, "/start")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "sam")
	tmpl := e.screen()
	mustContain(t, tmpl, "Pick a template")
	labels := ""
	for _, b := range e.buttons() {
		labels += b.Text + "|"
	}
	mustContain(t, labels, "Gold")
	mustContain(t, labels, "20.0 GB")
	mustContain(t, labels, "60 days")
	mustContain(t, labels, "No template")

	e.press(sudoUID, "Gold")
	summary := e.screen()
	mustContain(t, summary, "gold_sam") // the template's prefix is applied
	mustContain(t, summary, "Gold")
	mustContain(t, summary, "20.0 GB")
	mustContain(t, summary, "60 days")
	mustContain(t, summary, "vless")

	e.press(sudoUID, "Create user")
	u := e.userJSON("gold_sam")
	if u == nil {
		t.Fatal("template user was not created")
	}
	if num(u, "data_limit") != float64(20*gibI) {
		t.Errorf("data_limit = %v, want the template's 20 GB", u["data_limit"])
	}
	if exp, want := int64(num(u, "expire")), time.Now().Unix()+60*86400; exp < want-60 || exp > want+60 {
		t.Errorf("expire = %d, want about %d", exp, want)
	}
	if inb := u["inbounds"].(map[string]any)["vless"].([]any); len(inb) != 1 || inb[0] != "VLESS TCP" {
		t.Errorf("inbounds = %v, want the template's", u["inbounds"])
	}

	// "No template" keeps the manual path open even when templates exist.
	e.press(sudoUID, "Home")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "manual_one")
	e.press(sudoUID, "No template")
	mustContain(t, e.screen(), "Pick the data limit")
}

func TestNewUserWizardWithoutAnyInboundsSaysSo(t *testing.T) {
	e := newEnv(t) // no inbound seeded
	e.send(sudoUID, "/start")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "lonely_user")
	e.press(sudoUID, "5")
	e.press(sudoUID, "30")
	e.press(sudoUID, "Create user")
	mustContain(t, e.screen(), "No inbounds are configured")
	if e.userJSON("lonely_user") != nil {
		t.Error("a user without any proxy was created")
	}
}

// A wizard lives in Redis, not in the process: a restart, or a second backend
// taking over, must not lose a half-finished flow.
func TestWizardSurvivesAProcessRestart(t *testing.T) {
	e := newEnv(t)
	e.seedInbound()
	e.send(sudoUID, "/start")
	e.press(sudoUID, "New user")
	e.send(sudoUID, "resumed_user")
	e.press(sudoUID, "20")

	// A brand new console (fresh memory) answers the rest of the conversation.
	second := New(Deps{
		Queries: e.queries, Cache: e.cache, Issuer: e.issuer, SudoUsername: envSudoUsername,
		Settings: e.settings.get, Logger: e.console.d.Logger, BaseURL: e.tg.srv.URL,
	})
	second.authTTL = 0
	second.limitBurst, second.limitPerSec = 1000, 1000
	second.SetAPI(e.router)
	e.console = second

	mustContain(t, e.screen(), "Pick the duration")
	e.press(sudoUID, "90")
	mustContain(t, e.screen(), "resumed_user")
	e.press(sudoUID, "Create user")

	u := e.userJSON("resumed_user")
	if u == nil {
		t.Fatal("the resumed wizard did not create the user")
	}
	if num(u, "data_limit") != float64(20*gibI) {
		t.Errorf("data_limit = %v, want the 20 GB chosen before the restart", u["data_limit"])
	}
}
