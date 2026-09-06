package report

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/discord"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/telegram"
)

// capturedRequest and captureServer stand in for the real Telegram Bot API
// / Discord webhook endpoint - a real HTTP server, not a mock of this
// package's own logic, matching this project's "no mocking business logic"
// testing convention (see internal/httpapi/harness_test.go's testPool/
// testCache for the same philosophy applied to Postgres/Redis).
type capturedRequest struct {
	Method string
	Path   string
	Body   string
}

type captureServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
	// failNext, when > 0, makes that many subsequent requests respond 400 -
	// used to prove one bad recipient doesn't suppress the others.
	failNext int
}

func newCaptureServer() *captureServer {
	s := &captureServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		fail := s.failNext > 0
		if fail {
			s.failNext--
		}
		s.requests = append(s.requests, capturedRequest{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		s.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	return s
}

func (s *captureServer) Requests() []capturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testDispatcher wires a Dispatcher whose Telegram/Discord senders point at
// the given capture servers, with settingsFn returning vals directly (no DB
// involved - this package has no Postgres dependency of its own).
func testDispatcher(flags NotifyFlags, vals integrationsettings.Values, tgServer, dcServer *captureServer) *Dispatcher {
	httpClient := &http.Client{Timeout: 5 * time.Second}
	tgBaseURL := ""
	if tgServer != nil {
		tgBaseURL = tgServer.URL
	}
	settingsFn := func(ctx context.Context) (integrationsettings.Values, error) { return vals, nil }
	return New(flags, settingsFn, telegram.NewSender(httpClient, tgBaseURL), discord.NewSender(httpClient), testLogger())
}

func allFlags() NotifyFlags {
	return NotifyFlags{StatusChange: true, UserCreated: true, UserUpdated: true, UserDeleted: true, UserDataUsedReset: true, UserSubRevoked: true, Login: true}
}

func TestUserCreatedSendsTelegramAndDiscord(t *testing.T) {
	tg := newCaptureServer()
	defer tg.Close()
	dc := newCaptureServer()
	defer dc.Close()

	vals := integrationsettings.Values{TelegramAPIToken: "tok", TelegramAdminIDs: []int64{111}, DiscordWebhookURL: dc.URL}
	d := testDispatcher(allFlags(), vals, tg, dc)

	d.UserCreated(context.Background(), UserSummary{Username: "alice", Proxies: []string{"vless"}}, "sudo", nil)

	if got := len(tg.Requests()); got != 1 {
		t.Fatalf("telegram requests = %d, want 1", got)
	}
	if got := len(dc.Requests()); got != 1 {
		t.Fatalf("discord requests = %d, want 1", got)
	}
	if !strings.Contains(tg.Requests()[0].Body, "alice") {
		t.Errorf("telegram body missing username: %s", tg.Requests()[0].Body)
	}
	if !strings.Contains(dc.Requests()[0].Body, "alice") {
		t.Errorf("discord body missing username: %s", dc.Requests()[0].Body)
	}
}

func TestNotifyFlagFalseSendsNothing(t *testing.T) {
	tg := newCaptureServer()
	defer tg.Close()
	dc := newCaptureServer()
	defer dc.Close()

	vals := integrationsettings.Values{TelegramAPIToken: "tok", TelegramAdminIDs: []int64{111}, DiscordWebhookURL: dc.URL}
	flags := allFlags()
	flags.UserCreated = false
	d := testDispatcher(flags, vals, tg, dc)

	d.UserCreated(context.Background(), UserSummary{Username: "alice"}, "sudo", nil)

	if got := len(tg.Requests()); got != 0 {
		t.Errorf("telegram requests = %d, want 0 (NOTIFY_USER_CREATED=false)", got)
	}
	if got := len(dc.Requests()); got != 0 {
		t.Errorf("discord requests = %d, want 0 (NOTIFY_USER_CREATED=false)", got)
	}
}

func TestLoggerChannelPreferredOverAdminList(t *testing.T) {
	tg := newCaptureServer()
	defer tg.Close()
	dc := newCaptureServer()
	defer dc.Close()

	vals := integrationsettings.Values{
		TelegramAPIToken: "tok", TelegramAdminIDs: []int64{111, 222, 333}, TelegramLoggerChannelID: 999,
	}
	d := testDispatcher(allFlags(), vals, tg, dc)

	d.StatusChange(context.Background(), "bob", "limited", nil)

	reqs := tg.Requests()
	if len(reqs) != 1 {
		t.Fatalf("telegram requests = %d, want exactly 1 (logger channel only, not fanned out to 3 admins)", len(reqs))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got, want := body["chat_id"], float64(999); got != want {
		t.Errorf("chat_id = %v, want %v", got, want)
	}
}

func TestAdminListFanOutWhenNoLoggerChannel(t *testing.T) {
	tg := newCaptureServer()
	defer tg.Close()
	dc := newCaptureServer()
	defer dc.Close()

	vals := integrationsettings.Values{TelegramAPIToken: "tok", TelegramAdminIDs: []int64{111, 222, 333}}
	d := testDispatcher(allFlags(), vals, tg, dc)

	d.StatusChange(context.Background(), "bob", "limited", nil)

	if got := len(tg.Requests()); got != 3 {
		t.Fatalf("telegram requests = %d, want 3 (one per admin ID)", got)
	}
}

func TestOwningAdminGetsDirectMessageAndWebhook(t *testing.T) {
	tg := newCaptureServer()
	defer tg.Close()
	dc := newCaptureServer()
	defer dc.Close()
	adminHook := newCaptureServer()
	defer adminHook.Close()

	vals := integrationsettings.Values{TelegramAPIToken: "tok", TelegramAdminIDs: []int64{111}, DiscordWebhookURL: dc.URL}
	d := testDispatcher(allFlags(), vals, tg, dc)

	ownerTelegramID := int64(555)
	ownerWebhook := adminHook.URL
	userAdmin := &AdminRef{Username: "reseller1", TelegramID: &ownerTelegramID, DiscordWebhook: &ownerWebhook}

	d.StatusChange(context.Background(), "bob", "limited", userAdmin)

	// 1 fan-out to the single global admin ID + 1 DM to the owning admin.
	if got := len(tg.Requests()); got != 2 {
		t.Fatalf("telegram requests = %d, want 2 (1 global admin + 1 owning-admin DM)", got)
	}
	if got := len(dc.Requests()); got != 1 {
		t.Fatalf("global discord requests = %d, want 1", got)
	}
	if got := len(adminHook.Requests()); got != 1 {
		t.Fatalf("owning-admin discord webhook requests = %d, want 1", got)
	}
}

func TestOneBadTelegramRecipientDoesNotSuppressOthers(t *testing.T) {
	tg := newCaptureServer()
	defer tg.Close()
	dc := newCaptureServer()
	defer dc.Close()
	tg.failNext = 1 // the first of two admin sends fails with 400

	vals := integrationsettings.Values{TelegramAPIToken: "tok", TelegramAdminIDs: []int64{111, 222}}
	d := testDispatcher(allFlags(), vals, tg, dc)

	d.StatusChange(context.Background(), "bob", "limited", nil)

	// Both sends were still attempted, even though the first "failed" -
	// this project's Sender.Report never lets one bad recipient stop the
	// loop (the fix over Python's single try/except).
	if got := len(tg.Requests()); got != 2 {
		t.Fatalf("telegram requests = %d, want 2 (both attempted despite the first failing)", got)
	}
}

func TestLoginRedactsPassword(t *testing.T) {
	tg := newCaptureServer()
	defer tg.Close()
	dc := newCaptureServer()
	defer dc.Close()

	vals := integrationsettings.Values{TelegramAPIToken: "tok", TelegramAdminIDs: []int64{111}, DiscordWebhookURL: dc.URL}
	d := testDispatcher(allFlags(), vals, tg, dc)

	d.Login(context.Background(), "admin1", "203.0.113.5", "✅ Success")

	const secretPassword = "hunter2-super-secret"
	for _, req := range tg.Requests() {
		if strings.Contains(req.Body, secretPassword) || strings.Contains(strings.ToLower(req.Body), "password") {
			t.Errorf("telegram login body unexpectedly mentions a password field/value: %s", req.Body)
		}
	}
	for _, req := range dc.Requests() {
		if strings.Contains(req.Body, secretPassword) || strings.Contains(strings.ToLower(req.Body), "password") {
			t.Errorf("discord login body unexpectedly mentions a password field/value: %s", req.Body)
		}
	}
	if len(tg.Requests()) != 1 || len(dc.Requests()) != 1 {
		t.Fatalf("expected exactly one telegram and one discord request, got %d/%d", len(tg.Requests()), len(dc.Requests()))
	}
}

func TestStatusChangeOnHold(t *testing.T) {
	tg := newCaptureServer()
	defer tg.Close()
	dc := newCaptureServer()
	defer dc.Close()

	vals := integrationsettings.Values{TelegramAPIToken: "tok", TelegramAdminIDs: []int64{111}, DiscordWebhookURL: dc.URL}
	d := testDispatcher(allFlags(), vals, tg, dc)

	// Reproduces the KeyError-equivalent gap this port fixes: the Python
	// _status table has no "on_hold" entry, silently sending nothing.
	d.StatusChange(context.Background(), "carol", "on_hold", nil)

	if got := len(tg.Requests()); got != 1 {
		t.Fatalf("telegram requests = %d, want 1 (on_hold must still send a message)", got)
	}
	if got := len(dc.Requests()); got != 1 {
		t.Fatalf("discord requests = %d, want 1 (on_hold must still send a message)", got)
	}
	if !strings.Contains(tg.Requests()[0].Body, "OnHold") {
		t.Errorf("telegram on_hold message missing #OnHold label: %s", tg.Requests()[0].Body)
	}
}
