package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/kirbot"
	"github.com/legendary1205/rapido-go/internal/report"
	"github.com/legendary1205/rapido-go/internal/telegram"
)

const (
	testSudoUsername = "test-sudo"
	testSudoPassword = "test-sudo-password"
)

// testCertPEM/testKeyPEM are a real, valid, self-signed ECDSA cert/key pair
// (CN=example.test, 10-year validity) - used by any test that needs a real
// TLS inbound certificate rather than a placeholder string, so the round
// trip through storage/JSON/node-config actually exercises real PEM
// content. Never used for anything but tests; generated once and frozen
// here rather than regenerated per test run.
const testCertPEM = `-----BEGIN CERTIFICATE-----
MIIBXTCCAQOgAwIBAgIBATAKBggqhkjOPQQDAjAXMRUwEwYDVQQDEwxleGFtcGxl
LnRlc3QwHhcNMjYwOTA3MTI1MjM4WhcNMzYwOTA3MTM1MjM4WjAXMRUwEwYDVQQD
EwxleGFtcGxlLnRlc3QwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAATyGO/P9eCr
vTXXCTRjgfAF7ndDAU1SM+DTYBOj6rVaa9+tRvJ6l3vYpZtT2D2NMPBMzgVuV8iG
u8A9/JcE/irMo0AwPjAOBgNVHQ8BAf8EBAMCB4AwEwYDVR0lBAwwCgYIKwYBBQUH
AwEwFwYDVR0RBBAwDoIMZXhhbXBsZS50ZXN0MAoGCCqGSM49BAMCA0gAMEUCIQDq
gibaZwip7+t66D64WsXJ9SBOsKTq+L3t5Thc5bzaFQIgOC73EDy+FbfuttkW+X8N
hbY2Lo7pOEX6xRezdduatlo=
-----END CERTIFICATE-----
`

const testKeyPEM = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEICiESJZ2LnzP+hpr9U26Puzb5DoiaYNIH/vBsj4wAoDvoAoGCCqGSM49
AwEHoUQDQgAE8hjvz/Xgq7011wk0Y4HwBe53QwFNUjPg02ATo+q1WmvfrUbyepd7
2KWbU9g9jTDwTM4FblfIhrvAPfyXBP4qzA==
-----END EC PRIVATE KEY-----
`

// newTestRouter builds a full router (real Postgres, real JWT issuer) and
// returns it plus a bearer token for the env-bootstrapped sudo account.
// Skips if TEST_DATABASE_URL isn't set - see store_test.go's testPool.
func newTestRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	router, token, _ := newTestRouterAndHandler(t)
	return router, token
}

// newTestRouterAndHandler is newTestRouter plus the underlying *Handler -
// only backup_test.go needs the handler itself, to swap h.dumpDatabase for
// a fake that doesn't need the real pg_dump binary installed (see
// dumpDatabaseFn's doc comment in backup.go). Mutating h.dumpDatabase after
// the router is built still takes effect: every route calls a method on
// the same *Handler at request time, not at registration time.
func newTestRouterAndHandler(t *testing.T) (http.Handler, string, *Handler) {
	t.Helper()
	pool := testPool(t)
	truncateAll(t, pool)
	cacheClient := testCache(t)

	store := NewStore(pool, cacheClient)
	ensureTestCA(t, store)
	resetIntegrationSettings(t, pool)
	resetCoreConfig(t, pool)
	testSecret := []byte("test-secret")
	issuer := auth.NewTokenIssuer(testSecret, time.Hour)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Real Dispatcher/kirbot.Client, same as production - with no test env
	// vars and a freshly-reset (all-NULL) settings row, every method on
	// both is a documented no-op (see internal/report/report_test.go), so
	// existing tests get zero new outbound HTTP calls by default. A test
	// that wants to exercise notifications sets TELEGRAM_*/DISCORD_*/
	// KIRBOT_* via PUT /api/settings/integrations, typically pointing at a
	// local httptest.Server.
	envDefaults := integrationsettings.Values{}
	settingsFn := func(ctx context.Context) (integrationsettings.Values, error) {
		row, err := store.CachedGetIntegrationSettings(ctx)
		if err != nil {
			return integrationsettings.Values{}, err
		}
		return integrationsettings.Resolve(row, envDefaults), nil
	}
	notifyHTTPClient := &http.Client{Timeout: 5 * time.Second}
	dispatcher := report.New(report.NotifyFlags{
		StatusChange: true, UserCreated: true, UserUpdated: true, UserDeleted: true,
		UserDataUsedReset: true, UserSubRevoked: true, Login: true,
	}, settingsFn, telegram.NewSender(notifyHTTPClient, ""), discord.NewSender(notifyHTTPClient), logger)
	kirbotClient := kirbot.NewClient(&http.Client{Timeout: 5 * time.Second})

	handler := NewHandler(store, issuer, testSudoUsername, testSudoPassword, testSecret, "203.0.113.1", "", "", "", SubscriptionFormatFlags{},
		envDefaults, dispatcher, kirbotClient, nil, hostmetrics.NewPreviousTracker(),
		os.Getenv("TEST_DATABASE_URL"), t.TempDir(), 5, logger)
	router := NewRouter(handler, logger, []string{"*"})

	token, err := issuer.Issue(testSudoUsername, true)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return router, token, handler
}

// testCache connects to a real Redis instance and flushes its DB - point
// TEST_REDIS_ADDR at the same docker-compose redis service the Postgres
// tests already rely on (docker-compose.yml starts them together). Skips
// instead of failing when unset, matching testPool's convention - every
// test built on newTestRouter now needs Redis too, since Store always
// holds a Cache field; this is an accepted, deliberate consequence of the
// caching work, not an oversight.
func testCache(t *testing.T) *cache.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set, skipping test against a real Redis instance")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := cache.New(addr, "", 0, logger)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := c.Raw().FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("FlushDB: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// truncateAll clears every table Phase 2-3 tests touch, so each test starts
// from a clean slate regardless of what earlier tests (or the deployed
// panel sharing this same database over the SSH tunnel) left behind. `tls`
// is deliberately not truncated - ensureTestCA below makes it idempotent
// instead, since wiping the CA out from under a test that runs concurrently
// with... (tests in this package run sequentially, but re-generating a
// 4096-bit RSA CA per test is needlessly slow) is both unnecessary and slow.
func truncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"TRUNCATE admin_usage_logs, users, admins, inbounds, hosts, user_templates, nodes, gateway_peers, gateway_settings RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// resetIntegrationSettings clears the integration_settings singleton row
// back to every column NULL ("use env defaults") before each test - same
// idempotent-reset treatment as ensureTestCA gives the tls row, since this
// is also a seeded singleton a TRUNCATE would need to re-seed rather than
// just clear.
func resetIntegrationSettings(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE integration_settings SET
		kirbot_secret = NULL, kirbot_url = NULL, kirbot_license = NULL,
		telegram_api_token = NULL, telegram_admin_ids = NULL, telegram_proxy_url = NULL,
		telegram_logger_channel_id = NULL, telegram_logger_topic_id = NULL, telegram_default_vless_flow = NULL,
		webhook_addresses = NULL, webhook_secret = NULL, discord_webhook_url = NULL, updated_at = NULL`)
	if err != nil {
		t.Fatalf("reset integration_settings: %v", err)
	}
}

// resetCoreConfig resets the core_config singleton row back to its
// migration-seeded defaults before each test - same reasoning as
// resetIntegrationSettings: a TRUNCATE would need to re-seed a singleton
// rather than just clear it, so a plain UPDATE is simpler.
func resetCoreConfig(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE core_config SET
		log_level = 'warn', sniff_enabled = true, outbounds = '[]', routing_rules = '[]', dns_servers = '[]', updated_at = now()`)
	if err != nil {
		t.Fatalf("reset core_config: %v", err)
	}
}

// ensureTestCA mirrors cmd/panel/main.go's ensureTLS bootstrap (duplicated
// here rather than shared, since cmd/panel already imports this package -
// the reverse import would cycle): the tls table's one row doubles as the
// Rapido CA node certificates are issued from, so node tests need it to
// exist same as a real deployment does after its first boot.
func ensureTestCA(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Queries.GetTLS(ctx); err == nil {
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetTLS: %v", err)
	}
	pair, _, err := certs.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if _, err := store.Queries.CreateTLS(ctx, generated.CreateTLSParams{Key: pair.KeyPEM, Certificate: pair.CertPEM}); err != nil {
		t.Fatalf("CreateTLS: %v", err)
	}
}

type apiResponse struct {
	Code int
	Body map[string]interface{}
	Raw  []byte
}

func doRequest(t *testing.T, router http.Handler, method, path, token string, body interface{}) apiResponse {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var decoded map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return apiResponse{Code: rec.Code, Body: decoded, Raw: rec.Body.Bytes()}
}
