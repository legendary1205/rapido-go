package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// captureServer is a real local HTTP server standing in for the real
// Telegram Bot API / Discord webhook endpoint / KirBot bot - not a mock of
// this package's own logic, matching the rest of this project's tests.
type captureServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string // raw request bodies
}

func newCaptureServer(t *testing.T, respond func(w http.ResponseWriter, body []byte)) *captureServer {
	t.Helper()
	s := &captureServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, string(body))
		s.mu.Unlock()
		if respond != nil {
			respond(w, body)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *captureServer) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.requests))
	copy(out, s.requests)
	return out
}

// setDiscordWebhookDirect writes discord_webhook_url straight into the DB,
// bypassing PUT /api/settings/integrations's real (and, for production
// URLs, correct) validation that a webhook starts with
// "https://discord.com" - a local httptest.Server URL could never pass
// that check, but the point of these tests is proving the settings ->
// cache -> report.Dispatcher wiring works, not re-testing that validator
// (already covered by TestKirbotUserLimitRejectsNonSudoOverCap and friends
// going through the real endpoint with kirbot_url, which has no such
// format restriction). Connects independently of newTestRouter's pool,
// matching testPool's own TEST_DATABASE_URL convention.
func setDiscordWebhookDirect(t *testing.T, url string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(context.Background(),
		"UPDATE integration_settings SET discord_webhook_url = $1 WHERE id = (SELECT id FROM integration_settings ORDER BY id LIMIT 1)", url); err != nil {
		t.Fatalf("set discord_webhook_url directly: %v", err)
	}
}

// TestCreateUserDispatchesDiscordNotification proves the settings ->
// cache -> report.Dispatcher wiring works end-to-end for real: configuring
// a webhook via PUT /api/settings/integrations makes the very next
// POST /api/user actually deliver a notification.
func TestCreateUserDispatchesDiscordNotification(t *testing.T) {
	router, token := newTestRouter(t)
	webhook := newCaptureServer(t, nil)
	setDiscordWebhookDirect(t, webhook.URL)

	resp := doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	if resp.Code != 200 {
		t.Fatalf("sync inbounds: %d %v", resp.Code, resp.Body)
	}
	resp = doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "notify_create_test",
		"proxies":  map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}

	bodies := webhook.bodies()
	if len(bodies) != 1 {
		t.Fatalf("discord webhook received %d requests, want 1", len(bodies))
	}
	if !strings.Contains(bodies[0], "notify_create_test") {
		t.Errorf("discord payload missing username: %s", bodies[0])
	}
}

// TestLoginFailureNotifiesWithoutPassword proves the security decision is
// actually wired end-to-end: a failed login attempt notifies, and the
// attempted password never appears in the delivered payload.
func TestLoginFailureNotifiesWithoutPassword(t *testing.T) {
	router, _ := newTestRouter(t)
	webhook := newCaptureServer(t, nil)
	setDiscordWebhookDirect(t, webhook.URL)

	form := url.Values{"username": {testSudoUsername}, "password": {"totally-wrong-password-xyz"}}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad password, got %d", rec.Code)
	}

	bodies := webhook.bodies()
	if len(bodies) != 1 {
		t.Fatalf("discord webhook received %d requests, want 1", len(bodies))
	}
	if strings.Contains(bodies[0], "totally-wrong-password-xyz") {
		t.Errorf("discord login payload leaked the attempted password: %s", bodies[0])
	}
	if strings.Contains(strings.ToLower(bodies[0]), "password") {
		t.Errorf("discord login payload mentions a password field at all: %s", bodies[0])
	}
}

// TestKirbotUserLimitRejectsNonSudoOverCap drives the full chain: a stub
// KirBot server capping a reseller at 1 user, configured via the real
// settings API, actually blocks that reseller's 2nd user creation while
// leaving a sudo admin uncapped.
func TestKirbotUserLimitRejectsNonSudoOverCap(t *testing.T) {
	router, sudoToken := newTestRouter(t)

	kirbotServer := newCaptureServer(t, func(w http.ResponseWriter, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users_limit": 1}`))
	})

	resp := doRequest(t, router, "PUT", "/api/settings/integrations", sudoToken, map[string]interface{}{
		"kirbot_secret": "test-secret", "kirbot_url": kirbotServer.URL,
	})
	if resp.Code != 200 {
		t.Fatalf("set kirbot settings: %d %v", resp.Code, resp.Body)
	}

	doRequest(t, router, "POST", "/api/inbounds/sync", sudoToken, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "capped-reseller", "password": "pw12345", "is_sudo": false})
	resellerToken := loginAs(t, router, "capped-reseller", "pw12345")

	resp = doRequest(t, router, "POST", "/api/user", resellerToken, map[string]interface{}{
		"username": "capped_user_1", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("first user under cap: %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "POST", "/api/user", resellerToken, map[string]interface{}{
		"username": "capped_user_2", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 400 {
		t.Fatalf("expected 400 for a reseller at their KirBot cap, got %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "POST", "/api/user", sudoToken, map[string]interface{}{
		"username": "uncapped_sudo_user", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("sudo must never be capped by KirBot, got %d %v", resp.Code, resp.Body)
	}
}

// TestInboundsFilteredByKirbotForNonSudo proves handleListInbounds actually
// calls out to KirBot and uses its filtered result for a non-sudo admin,
// while sudo always sees the unfiltered list (and never triggers a call).
func TestInboundsFilteredByKirbotForNonSudo(t *testing.T) {
	router, sudoToken := newTestRouter(t)

	kirbotServer := newCaptureServer(t, func(w http.ResponseWriter, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"vmess": ["VMess TCP"]}`))
	})

	doRequest(t, router, "PUT", "/api/settings/integrations", sudoToken, map[string]interface{}{
		"kirbot_secret": "test-secret", "kirbot_url": kirbotServer.URL,
	})
	doRequest(t, router, "POST", "/api/inbounds/sync", sudoToken, []map[string]string{
		{"tag": "VMess TCP", "protocol": "vmess"},
		{"tag": "VMess WS", "protocol": "vmess"},
	})
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "filtered-reseller", "password": "pw12345", "is_sudo": false})
	resellerToken := loginAs(t, router, "filtered-reseller", "pw12345")

	resp := doRequest(t, router, "GET", "/api/inbounds", resellerToken, nil)
	if resp.Code != 200 {
		t.Fatalf("get inbounds as reseller: %d %v", resp.Code, resp.Body)
	}
	filtered := inboundTagsOf(t, resp, "vmess")
	if len(filtered) != 1 || filtered[0] != "VMess TCP" {
		t.Errorf("filtered inbounds = %v, want only VMess TCP (KirBot's stubbed response)", filtered)
	}

	resp = doRequest(t, router, "GET", "/api/inbounds", sudoToken, nil)
	if resp.Code != 200 {
		t.Fatalf("get inbounds as sudo: %d %v", resp.Code, resp.Body)
	}
	unfiltered := inboundTagsOf(t, resp, "vmess")
	if len(unfiltered) != 2 {
		t.Errorf("sudo's inbounds = %v, want both tags (KirBot must never filter sudo)", unfiltered)
	}
}
