package reviewjob

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

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

func TestReviewFlipsLimitedUser(t *testing.T) {
	h := newTestHarness(t)
	u := h.createTestUser(t, "limited_user", "active", 1000, 1000, 0)

	if err := review(context.Background(), h.q, testLogger()); err != nil {
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

	if err := review(context.Background(), h.q, testLogger()); err != nil {
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

	if err := review(context.Background(), h.q, testLogger()); err != nil {
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

	if err := review(context.Background(), h.q, testLogger()); err != nil {
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

	if err := review(context.Background(), h.q, testLogger()); err != nil {
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

	if err := review(context.Background(), h.q, testLogger()); err != nil {
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

	if err := review(context.Background(), h.q, testLogger()); err != nil {
		t.Fatalf("review: %v", err)
	}
	got, _ := h.q.GetUserByID(context.Background(), u.ID)
	if got.Status != "on_hold" {
		t.Errorf("status = %q, want still on_hold", got.Status)
	}
}
