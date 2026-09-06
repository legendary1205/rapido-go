package reviewjob

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/discord"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/report"
	"github.com/legendary1205/rapido-go/internal/telegram"
)

// captureServer is a real local HTTP server standing in for a Discord
// webhook endpoint - matching internal/report/report_test.go's and
// internal/httpapi/notifications_test.go's identical helper (not shared
// across packages, since each is a small, self-contained test fixture).
type captureServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()
	s := &captureServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, string(body))
		s.mu.Unlock()
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

// testHarness bundles the generated query wrapper with the raw pool - a
// couple of test fixtures below need to poke columns (used_traffic,
// online_at, on_hold_timeout) no CRUD query exposes, since real
// traffic/heartbeat accounting is a later phase's job.
type testHarness struct {
	q    *generated.Queries
	pool *pgxpool.Pool
}

// newTestHarness connects to a real Postgres instance and truncates the
// tables this job touches - point TEST_DATABASE_URL at the same
// docker-compose postgres service the other packages' tests use.
func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping test against a real Postgres instance")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), "TRUNCATE users, next_plans, user_usage_logs, node_user_usages RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return &testHarness{q: generated.New(pool), pool: pool}
}

func (h *testHarness) execRaw(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("execRaw(%s): %v", sql, err)
	}
}

func (h *testHarness) createTestUser(t *testing.T, username, status string, dataLimit, usedTraffic, expire int64) generated.User {
	t.Helper()
	u, err := h.q.CreateUser(context.Background(), generated.CreateUserParams{
		Username: username, Status: status,
		DataLimit: pgInt8(dataLimit), Expire: pgInt4(expire), DataLimitResetStrategy: "no_reset",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if usedTraffic != 0 {
		h.execRaw(t, "UPDATE users SET used_traffic = $2 WHERE id = $1", u.ID, usedTraffic)
		u.UsedTraffic = usedTraffic
	}
	return u
}

func pgInt8(n int64) pgtype.Int8 {
	if n == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: n, Valid: true}
}

func pgInt4(n int64) pgtype.Int4 {
	if n == 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(n), Valid: true}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testDispatcher builds a real report.Dispatcher (no mocks, matching this
// project's testing convention) against the test DB's real (freshly
// migrated, all-NULL) integration_settings row. With no env vars or DB
// overrides set, every Telegram/Discord call inside it is a documented
// no-op - see internal/report/report_test.go for tests that actually
// exercise notification delivery against local httptest.Server stand-ins.
func testDispatcher(q *generated.Queries) *report.Dispatcher {
	settingsFn := func(ctx context.Context) (integrationsettings.Values, error) {
		row, err := q.GetIntegrationSettings(ctx)
		if err != nil {
			return integrationsettings.Values{}, err
		}
		return integrationsettings.Resolve(row, integrationsettings.Values{}), nil
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	return report.New(report.NotifyFlags{
		StatusChange: true, UserCreated: true, UserUpdated: true, UserDeleted: true,
		UserDataUsedReset: true, UserSubRevoked: true, Login: true,
	}, settingsFn, telegram.NewSender(httpClient, ""), discord.NewSender(httpClient), testLogger())
}

func TestReviewFlipsLimitedUser(t *testing.T) {
	h := newTestHarness(t)
	u := h.createTestUser(t, "limited_user", "active", 1000, 1000, 0)

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, err := h.q.GetUserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.Status != "limited" {
		t.Errorf("status = %q, want limited", got.Status)
	}
	if !got.LastStatusChange.Valid {
		t.Error("last_status_change was not stamped")
	}
}

func TestReviewFlipsExpiredUser(t *testing.T) {
	h := newTestHarness(t)
	past := time.Now().Add(-time.Hour).Unix()
	u := h.createTestUser(t, "expired_user", "active", 0, 0, past)

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, _ := h.q.GetUserByID(context.Background(), u.ID)
	if got.Status != "expired" {
		t.Errorf("status = %q, want expired", got.Status)
	}
}

func TestReviewIgnoresZeroDataLimitAndExpire(t *testing.T) {
	// data_limit=0/expire=0 mean "unlimited", not "immediately due" - a
	// user with sky-high used_traffic but data_limit=0 must never be
	// flagged, matching the SQL's explicit `> 0` / `!= 0` guards.
	h := newTestHarness(t)
	u := h.createTestUser(t, "unlimited_user", "active", 0, 999999999, 0)

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, _ := h.q.GetUserByID(context.Background(), u.ID)
	if got.Status != "active" {
		t.Errorf("status = %q, want still active", got.Status)
	}
}

func TestReviewFiresNextPlanOnFireOnEither(t *testing.T) {
	h := newTestHarness(t)
	u := h.createTestUser(t, "plan_user", "active", 1000, 1000, 0)
	_, err := h.q.UpsertNextPlan(context.Background(), generated.UpsertNextPlanParams{
		UserID: u.ID, DataLimit: 5000, Expire: pgInt4(2592000), AddRemainingTraffic: false, FireOnEither: true,
	})
	if err != nil {
		t.Fatalf("UpsertNextPlan: %v", err)
	}

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, _ := h.q.GetUserByID(context.Background(), u.ID)
	if got.Status != "active" {
		t.Errorf("status = %q, want active (reactivated by the fired plan)", got.Status)
	}
	if got.UsedTraffic != 0 {
		t.Errorf("used_traffic = %d, want reset to 0", got.UsedTraffic)
	}
	// add_remaining_traffic=false: new limit = plan's 5000 + (old 1000 - used 1000) = 5000.
	if !got.DataLimit.Valid || got.DataLimit.Int64 != 5000 {
		t.Errorf("data_limit = %v, want 5000", got.DataLimit)
	}
	if _, err := h.q.GetNextPlanByUserID(context.Background(), u.ID); err == nil {
		t.Error("next_plan still exists after firing, want deleted")
	}
}

func TestReviewDoesNotFireNextPlanWhenOnlyOneConditionAndFireOnEitherFalse(t *testing.T) {
	h := newTestHarness(t)
	u := h.createTestUser(t, "plan_user_2", "active", 1000, 1000, 0) // limited only, not expired
	_, err := h.q.UpsertNextPlan(context.Background(), generated.UpsertNextPlanParams{
		UserID: u.ID, DataLimit: 5000, Expire: pgInt4(2592000), AddRemainingTraffic: false, FireOnEither: false,
	})
	if err != nil {
		t.Fatalf("UpsertNextPlan: %v", err)
	}

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, _ := h.q.GetUserByID(context.Background(), u.ID)
	if got.Status != "limited" {
		t.Errorf("status = %q, want limited (plan should not fire on just one condition)", got.Status)
	}
	if _, err := h.q.GetNextPlanByUserID(context.Background(), u.ID); err != nil {
		t.Error("next_plan was consumed even though it shouldn't have fired")
	}
}

func TestReviewActivatesOnHoldUserWhoConnected(t *testing.T) {
	h := newTestHarness(t)
	u := h.createTestUser(t, "onhold_connected", "on_hold", 0, 0, 0)
	h.execRaw(t, "UPDATE users SET on_hold_expire_duration = 86400, online_at = now() WHERE id = $1", u.ID)

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, _ := h.q.GetUserByID(context.Background(), u.ID)
	if got.Status != "active" {
		t.Errorf("status = %q, want active", got.Status)
	}
	if got.OnHoldExpireDuration.Valid || got.OnHoldTimeout.Valid {
		t.Error("on_hold fields were not cleared after activation")
	}
	if !got.Expire.Valid || got.Expire.Int32 == 0 {
		t.Error("expire was not set from on_hold_expire_duration")
	}
}

func TestReviewLeavesOnHoldUserWithNoActivityAlone(t *testing.T) {
	h := newTestHarness(t)
	u := h.createTestUser(t, "onhold_waiting", "on_hold", 0, 0, 0)
	h.execRaw(t, "UPDATE users SET on_hold_expire_duration = 86400, on_hold_timeout = now() + interval '1 day' WHERE id = $1", u.ID)

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, _ := h.q.GetUserByID(context.Background(), u.ID)
	if got.Status != "on_hold" {
		t.Errorf("status = %q, want still on_hold", got.Status)
	}
}

// setDiscordWebhook points the shared integration_settings row's global
// webhook at a local capture server - the DB-level equivalent of
// PUT /api/settings/integrations, since this package has no HTTP layer of
// its own to drive that endpoint through.
func (h *testHarness) setDiscordWebhook(t *testing.T, url string) {
	t.Helper()
	h.execRaw(t, "UPDATE integration_settings SET discord_webhook_url = $1 WHERE id = (SELECT id FROM integration_settings ORDER BY id LIMIT 1)", url)
	t.Cleanup(func() {
		h.execRaw(t, "UPDATE integration_settings SET discord_webhook_url = NULL WHERE id = (SELECT id FROM integration_settings ORDER BY id LIMIT 1)")
	})
}

func TestStatusChangeFiresReportOnLimitedTransition(t *testing.T) {
	h := newTestHarness(t)
	webhook := newCaptureServer(t)
	h.setDiscordWebhook(t, webhook.URL)
	h.createTestUser(t, "reported_limited_user", "active", 1000, 1000, 0)

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}

	bodies := webhook.bodies()
	if len(bodies) != 1 {
		t.Fatalf("discord webhook received %d requests, want 1 (the limited status_change)", len(bodies))
	}
	if !strings.Contains(bodies[0], "reported_limited_user") {
		t.Errorf("discord payload missing username: %s", bodies[0])
	}
}

func TestStatusChangeFiresReportOnNextPlanFire(t *testing.T) {
	h := newTestHarness(t)
	webhook := newCaptureServer(t)
	h.setDiscordWebhook(t, webhook.URL)
	u := h.createTestUser(t, "reported_plan_user", "active", 1000, 1000, 0)
	if _, err := h.q.UpsertNextPlan(context.Background(), generated.UpsertNextPlanParams{
		UserID: u.ID, DataLimit: 5000, Expire: pgInt4(2592000), AddRemainingTraffic: false, FireOnEither: true,
	}); err != nil {
		t.Fatalf("UpsertNextPlan: %v", err)
	}

	if err := review(context.Background(), h.q, testDispatcher(h.q), testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}

	bodies := webhook.bodies()
	if len(bodies) != 1 {
		t.Fatalf("discord webhook received %d requests, want 1 (the AutoReset report)", len(bodies))
	}
	if !strings.Contains(bodies[0], "reported_plan_user") {
		t.Errorf("discord payload missing username: %s", bodies[0])
	}
	if !strings.Contains(bodies[0], "AutoReset") {
		t.Errorf("discord payload missing AutoReset marker: %s", bodies[0])
	}
}
