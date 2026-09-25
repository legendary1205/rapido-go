package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/certs"
	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/discord"
	"github.com/legendary1205/rapido-go/internal/hostmetrics"
	"github.com/legendary1205/rapido-go/internal/httpapi"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/report"
	"github.com/legendary1205/rapido-go/internal/resellerapi"
	"github.com/legendary1205/rapido-go/internal/telegram"
)

const (
	envSudoUsername = "test-sudo"
	envToken        = "111111:TEST-bot-token"

	sudoUID     = int64(1001) // on the settings' admin list
	strangerUID = int64(9999) // on no list, bound to no admin
)

type mutableSettings struct {
	mu sync.Mutex
	v  integrationsettings.Values
}

func (m *mutableSettings) get(context.Context) (integrationsettings.Values, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.v, nil
}

func (m *mutableSettings) set(f func(*integrationsettings.Values)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(&m.v)
}

type testEnv struct {
	t         *testing.T
	pool      *pgxpool.Pool
	cache     *cache.Client
	queries   *generated.Queries
	router    http.Handler
	issuer    *auth.TokenIssuer
	sudoToken string
	settings  *mutableSettings
	console   *Console
	tg        *fakeTG
	bot       *botClient
	seq       int64
	lang      string       // Telegram language_code the simulated admins send
	handler   http.Handler // what the console talks to; wrapped by tests that fake a subprocess
}

// newEnv builds the real panel router over the shared test database and Redis
// and a console wired to a fake Bot API. Like the httpapi tests it clears the
// tables it uses, so run it only through the shared-database runner.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn, redisAddr := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("TEST_DATABASE_URL / TEST_REDIS_ADDR not set, skipping test against real Postgres and Redis")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE admin_usage_logs, users, admins, inbounds, hosts, user_templates, nodes, gateway_peers, gateway_settings RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE integration_settings SET
		reseller_api_secret = NULL, reseller_api_url = NULL, reseller_api_license = NULL,
		telegram_api_token = NULL, telegram_admin_ids = NULL, telegram_proxy_url = NULL,
		telegram_logger_channel_id = NULL, telegram_logger_topic_id = NULL, telegram_default_vless_flow = NULL,
		webhook_addresses = NULL, webhook_secret = NULL, discord_webhook_url = NULL, updated_at = NULL`); err != nil {
		t.Fatalf("reset integration_settings: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cacheClient := cache.New(redisAddr, "", 0, quiet)
	if err := cacheClient.Ping(ctx); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	if err := cacheClient.Raw().FlushDB(ctx).Err(); err != nil {
		t.Fatalf("FlushDB: %v", err)
	}
	t.Cleanup(func() { cacheClient.Close() })

	queries := generated.New(pool)
	if _, err := queries.GetTLS(ctx); errors.Is(err, pgx.ErrNoRows) {
		pair, _, gerr := certs.GenerateCA()
		if gerr != nil {
			t.Fatalf("GenerateCA: %v", gerr)
		}
		if _, err := queries.CreateTLS(ctx, generated.CreateTLSParams{Key: pair.KeyPEM, Certificate: pair.CertPEM}); err != nil {
			t.Fatalf("CreateTLS: %v", err)
		}
	} else if err != nil {
		t.Fatalf("GetTLS: %v", err)
	}

	secret := []byte("test-secret")
	issuer := auth.NewTokenIssuer(secret, time.Hour)
	store := httpapi.NewStore(pool, cacheClient)
	settingsFn := func(ctx context.Context) (integrationsettings.Values, error) {
		row, err := store.CachedGetIntegrationSettings(ctx)
		if err != nil {
			return integrationsettings.Values{}, err
		}
		return integrationsettings.Resolve(row, integrationsettings.Values{}), nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	dispatcher := report.New(report.NotifyFlags{
		StatusChange: true, UserCreated: true, UserUpdated: true, UserDeleted: true,
		UserDataUsedReset: true, UserSubRevoked: true, Login: true,
	}, settingsFn, telegram.NewSender(client, ""), discord.NewSender(client), quiet)
	handler := httpapi.NewHandler(store, issuer, envSudoUsername, "test-sudo-password", secret, "203.0.113.1",
		"https://panel.test", "", "", httpapi.SubscriptionFormatFlags{}, httpapi.SubscriptionBranding{},
		integrationsettings.Values{}, dispatcher, resellerapi.NewClient(client), nil, hostmetrics.NewPreviousTracker(),
		dsn, t.TempDir(), 5, quiet).WithSubscriptionURLPrefixes([]string{"https://panel.test", "https://alt.test"})
	router := httpapi.NewRouter(handler, quiet, []string{"*"})

	sudoToken, err := issuer.Issue(envSudoUsername, true)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	e := &testEnv{
		t: t, pool: pool, cache: cacheClient, queries: queries, router: router, handler: router,
		issuer: issuer, sudoToken: sudoToken,
		settings: &mutableSettings{v: integrationsettings.Values{
			TelegramAPIToken: envToken, TelegramAdminIDs: []int64{sudoUID},
		}},
		tg: newFakeTG(t, envToken), lang: "en",
	}
	e.console = New(Deps{
		Queries: queries, Cache: cacheClient, Issuer: issuer, SudoUsername: envSudoUsername,
		Settings: e.settings.get, Logger: quiet, BaseURL: e.tg.srv.URL,
	})
	e.console.authTTL = 0
	e.console.limitBurst, e.console.limitPerSec = 1000, 1000
	e.console.SetAPI(routerFunc(func(w http.ResponseWriter, r *http.Request) { e.handler.ServeHTTP(w, r) }))
	e.bot = newBotClient(envToken, e.tg.srv.URL, "")
	return e
}

type routerFunc func(http.ResponseWriter, *http.Request)

func (f routerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) { f(w, r) }

// ---- direct API access, used to seed data and to assert on the result ----

func (e *testEnv) api(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *testEnv) mustAPI(method, path, token string, body any) map[string]any {
	e.t.Helper()
	code, out := e.api(method, path, token, body)
	if code != http.StatusOK {
		e.t.Fatalf("%s %s = %d %v", method, path, code, out)
	}
	return out
}

func (e *testEnv) seedInbound() {
	e.t.Helper()
	e.mustAPI("POST", "/api/inbounds/sync", e.sudoToken, []map[string]string{{"tag": "VLESS TCP", "protocol": "vless"}})
}

func (e *testEnv) seedUser(name string, extra map[string]any) map[string]any {
	e.t.Helper()
	return e.seedUserAs(e.sudoToken, name, extra)
}

func (e *testEnv) seedUserAs(token, name string, extra map[string]any) map[string]any {
	e.t.Helper()
	body := map[string]any{"username": name, "proxies": map[string]any{"vless": map[string]any{}}}
	for k, v := range extra {
		body[k] = v
	}
	return e.mustAPI("POST", "/api/user", token, body)
}

// seedAdmin creates a regular admin bound to a Telegram account and returns a
// token for calling the API as that admin.
func (e *testEnv) seedAdmin(username string, telegramID int64, sudo bool) string {
	e.t.Helper()
	e.mustAPI("POST", "/api/admin", e.sudoToken, map[string]any{
		"username": username, "password": "pw-" + username + "-123", "is_sudo": sudo, "telegram_id": telegramID,
	})
	tok, err := e.issuer.Issue(username, sudo)
	if err != nil {
		e.t.Fatalf("Issue: %v", err)
	}
	return tok
}

// userJSON reads a user straight through the API as sudo; a nil result means the
// user does not exist.
func (e *testEnv) userJSON(name string) map[string]any {
	e.t.Helper()
	code, out := e.api("GET", "/api/user/"+url.PathEscape(name), e.sudoToken, nil)
	if code == http.StatusNotFound {
		return nil
	}
	if code != http.StatusOK {
		e.t.Fatalf("GET user %s = %d %v", name, code, out)
	}
	return out
}

// ---- driving the console like Telegram would ----

func (e *testEnv) nextID() int64 { e.seq++; return e.seq }

func (e *testEnv) dispatch(u tgUpdate) {
	e.t.Helper()
	e.console.dispatch(context.Background(), e.bot, u)
	e.console.inflight.Wait()
}

func (e *testEnv) send(uid int64, text string) { e.sendLang(uid, text, e.lang) }

func (e *testEnv) sendLang(uid int64, text, langCode string) {
	e.t.Helper()
	e.dispatch(tgUpdate{UpdateID: e.nextID(), Message: &tgMessage{
		MessageID: int(e.nextID()) + 5000,
		From:      &tgUser{ID: uid, LanguageCode: langCode},
		Chat:      tgChat{ID: uid, Type: "private"},
		Text:      text,
	}})
}

func (e *testEnv) tap(uid int64, data string) {
	e.t.Helper()
	e.dispatch(tgUpdate{UpdateID: e.nextID(), CallbackQuery: &tgCallback{
		ID: "cb" + itoa(int(e.seq)), From: tgUser{ID: uid, LanguageCode: e.lang}, Data: data,
		Message: &tgMessage{MessageID: e.tg.lastMessageID(), Chat: tgChat{ID: uid, Type: "private"}},
	}})
}

// press taps the button on the current screen whose text contains label.
func (e *testEnv) press(uid int64, label string) {
	e.t.Helper()
	_, buttons := e.tg.lastScreen()
	for _, b := range buttons {
		if strings.Contains(b.Text, label) {
			e.tap(uid, b.Data)
			return
		}
	}
	e.t.Fatalf("no button containing %q on screen %v", label, buttons)
}

func (e *testEnv) screen() string {
	text, _ := e.tg.lastScreen()
	return text
}

func (e *testEnv) buttons() []fakeButton {
	_, b := e.tg.lastScreen()
	return b
}

func (e *testEnv) hasButtonData(data string) bool {
	for _, b := range e.buttons() {
		if b.Data == data {
			return true
		}
	}
	return false
}

func (e *testEnv) hasButtonPrefix(prefix string) bool {
	for _, b := range e.buttons() {
		if strings.HasPrefix(b.Data, prefix) {
			return true
		}
	}
	return false
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected %q in:\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("did not expect %q in:\n%s", needle, haystack)
	}
}
