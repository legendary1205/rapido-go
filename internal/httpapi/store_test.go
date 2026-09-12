package httpapi

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// testPool connects to a real Postgres instance carrying the applied
// 00001_init_schema.sql migration - point TEST_DATABASE_URL at the
// docker-compose postgres service (see docker-compose.yml). Skips instead
// of failing when unset, so `go test ./...` still runs everywhere else.
func testPool(t testing.TB) *pgxpool.Pool {
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
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	return pool
}

// truncateAdmins clears every table admin-related tests touch, so each test
// starts from a clean slate regardless of what earlier tests left behind.
func truncateAdmins(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE admin_usage_logs, users, admins RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func TestCreateAndGetAdmin(t *testing.T) {
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	created, err := q.CreateAdmin(ctx, generated.CreateAdminParams{
		Username:       "alice",
		HashedPassword: "hashed",
		IsSudo:         true,
		TelegramID:     int8FromPtr(nil),
		DiscordWebhook: textFromPtr(nil),
	})
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if created.ID == 0 {
		t.Error("CreateAdmin returned zero ID")
	}
	if !created.IsSudo {
		t.Error("IsSudo = false, want true")
	}

	got, err := q.GetAdminByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetAdminByUsername: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetAdminByUsername returned ID %d, want %d", got.ID, created.ID)
	}

	byID, err := q.GetAdminByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAdminByID: %v", err)
	}
	if byID.Username != "alice" {
		t.Errorf("GetAdminByID returned username %q, want %q", byID.Username, "alice")
	}
}

func TestCreateAdminDuplicateUsernameRejected(t *testing.T) {
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	params := generated.CreateAdminParams{Username: "bob", HashedPassword: "h"}
	if _, err := q.CreateAdmin(ctx, params); err != nil {
		t.Fatalf("first CreateAdmin: %v", err)
	}
	if _, err := q.CreateAdmin(ctx, params); err == nil {
		t.Error("second CreateAdmin with the same username succeeded, want a uniqueness violation")
	}
}

func TestUsernameUniquenessIsCaseSensitive(t *testing.T) {
	// The whole point of dropping SQLite's dev-only NOCASE collation: distinct
	// case variants of a username must be allowed to coexist, matching
	// production MySQL's utf8mb4_bin behavior.
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	if _, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "Carol", HashedPassword: "h"}); err != nil {
		t.Fatalf("CreateAdmin(Carol): %v", err)
	}
	if _, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "carol", HashedPassword: "h"}); err != nil {
		t.Errorf("CreateAdmin(carol) after Carol failed, want it to succeed as a distinct username: %v", err)
	}
}

func TestListAdminsFilterAndPagination(t *testing.T) {
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	for _, u := range []string{"admin-one", "admin-two", "other"} {
		if _, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: u, HashedPassword: "h"}); err != nil {
			t.Fatalf("CreateAdmin(%s): %v", u, err)
		}
	}

	filtered, err := q.ListAdmins(ctx, generated.ListAdminsParams{Username: textFromPtr(strPtr("admin"))})
	if err != nil {
		t.Fatalf("ListAdmins filtered: %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("filtered ListAdmins returned %d rows, want 2", len(filtered))
	}

	limited, err := q.ListAdmins(ctx, generated.ListAdminsParams{Limit: pgInt4FromInt(1)})
	if err != nil {
		t.Fatalf("ListAdmins limited: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("limited ListAdmins returned %d rows, want 1", len(limited))
	}

	all, err := q.ListAdmins(ctx, generated.ListAdminsParams{})
	if err != nil {
		t.Fatalf("ListAdmins unfiltered: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("unfiltered ListAdmins returned %d rows, want 3", len(all))
	}
}

func TestUpdateAndDeleteAdmin(t *testing.T) {
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	admin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "dave", HashedPassword: "h1"})
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	updated, err := q.UpdateAdmin(ctx, generated.UpdateAdminParams{
		ID:             admin.ID,
		IsSudo:         true,
		HashedPassword: "h2",
		TelegramID:     int8FromPtr(int64Ptr(12345)),
		DiscordWebhook: admin.DiscordWebhook,
	})
	if err != nil {
		t.Fatalf("UpdateAdmin: %v", err)
	}
	if !updated.IsSudo {
		t.Error("UpdateAdmin did not persist is_sudo=true")
	}
	if updated.HashedPassword != "h2" {
		t.Errorf("HashedPassword = %q, want %q", updated.HashedPassword, "h2")
	}
	if got := int8ToPtr(updated.TelegramID); got == nil || *got != 12345 {
		t.Errorf("TelegramID = %v, want 12345", got)
	}

	if err := q.DeleteAdmin(ctx, admin.ID); err != nil {
		t.Fatalf("DeleteAdmin: %v", err)
	}
	if _, err := q.GetAdminByID(ctx, admin.ID); err == nil {
		t.Error("GetAdminByID succeeded after DeleteAdmin, want an error")
	}
}

func TestGetInactiveAdminsExcludesSudoAndRecentActivity(t *testing.T) {
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	oldAdmin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "stale-admin", HashedPassword: "h"})
	if err != nil {
		t.Fatalf("CreateAdmin(stale): %v", err)
	}
	// Backdate created_at directly - CreateAdmin always stamps now().
	if _, err := pool.Exec(ctx, "UPDATE admins SET created_at = now() - interval '400 days' WHERE id = $1", oldAdmin.ID); err != nil {
		t.Fatalf("backdate stale admin: %v", err)
	}

	freshAdmin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "fresh-admin", HashedPassword: "h"})
	if err != nil {
		t.Fatalf("CreateAdmin(fresh): %v", err)
	}
	_ = freshAdmin

	sudoAdmin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "sudo-admin", HashedPassword: "h", IsSudo: true})
	if err != nil {
		t.Fatalf("CreateAdmin(sudo): %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE admins SET created_at = now() - interval '400 days' WHERE id = $1", sudoAdmin.ID); err != nil {
		t.Fatalf("backdate sudo admin: %v", err)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -90)
	rows, err := q.GetInactiveAdmins(ctx, timestamptzFromTime(cutoff))
	if err != nil {
		t.Fatalf("GetInactiveAdmins: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("GetInactiveAdmins returned %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Username != "stale-admin" {
		t.Errorf("inactive admin = %q, want %q (fresh admin and sudo admin must be excluded)", rows[0].Username, "stale-admin")
	}
	if rows[0].UserCount != 0 {
		t.Errorf("UserCount = %d, want 0", rows[0].UserCount)
	}
}

func strPtr(s string) *string { return &s }
func int64Ptr(n int64) *int64 { return &n }

func TestDisableActiveUsersByAdminIDOnlyTouchesThatAdminsActiveAndOnHoldUsers(t *testing.T) {
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	admin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "reseller", HashedPassword: "h"})
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	otherAdmin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "other-reseller", HashedPassword: "h"})
	if err != nil {
		t.Fatalf("CreateAdmin(other): %v", err)
	}

	mustCreateUser := func(username, status string, adminID int32) generated.User {
		t.Helper()
		u, err := q.CreateUser(ctx, generated.CreateUserParams{
			Username:               username,
			Status:                 status,
			DataLimitResetStrategy: "no_reset",
			AdminID:                pgInt4FromInt(int(adminID)),
		})
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", username, err)
		}
		return u
	}

	active := mustCreateUser("active-user", statusActive, admin.ID)
	onHold := mustCreateUser("on-hold-user", statusOnHold, admin.ID)
	alreadyDisabled := mustCreateUser("already-disabled", statusDisabled, admin.ID)
	expired := mustCreateUser("expired-user", "expired", admin.ID)
	otherAdminsActive := mustCreateUser("other-active", statusActive, otherAdmin.ID)

	affected, err := q.DisableActiveUsersByAdminID(ctx, pgInt4FromInt(int(admin.ID)))
	if err != nil {
		t.Fatalf("DisableActiveUsersByAdminID: %v", err)
	}
	if len(affected) != 2 {
		t.Fatalf("affected %d users, want 2 (active + on_hold): %+v", len(affected), affected)
	}

	assertStatus := func(id int32, want string) {
		t.Helper()
		u, err := q.GetUserByID(ctx, id)
		if err != nil {
			t.Fatalf("GetUserByID(%d): %v", id, err)
		}
		if u.Status != want {
			t.Errorf("user %d status = %q, want %q", id, u.Status, want)
		}
	}
	assertStatus(active.ID, statusDisabled)
	assertStatus(onHold.ID, statusDisabled)
	assertStatus(alreadyDisabled.ID, statusDisabled) // unchanged, still disabled
	assertStatus(expired.ID, "expired")              // untouched - not active/on_hold
	assertStatus(otherAdminsActive.ID, statusActive)  // untouched - different admin
}

func TestActivateDisabledUsersByAdminIDOnlyTouchesThatAdminsDisabledUsers(t *testing.T) {
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	admin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "reseller", HashedPassword: "h"})
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	otherAdmin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "other-reseller", HashedPassword: "h"})
	if err != nil {
		t.Fatalf("CreateAdmin(other): %v", err)
	}

	mustCreateUser := func(username, status string, adminID int32) generated.User {
		t.Helper()
		u, err := q.CreateUser(ctx, generated.CreateUserParams{
			Username:               username,
			Status:                 status,
			DataLimitResetStrategy: "no_reset",
			AdminID:                pgInt4FromInt(int(adminID)),
		})
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", username, err)
		}
		return u
	}

	disabled := mustCreateUser("disabled-user", statusDisabled, admin.ID)
	alreadyActive := mustCreateUser("already-active", statusActive, admin.ID)
	expired := mustCreateUser("expired-user", "expired", admin.ID)
	otherAdminsDisabled := mustCreateUser("other-disabled", statusDisabled, otherAdmin.ID)

	affected, err := q.ActivateDisabledUsersByAdminID(ctx, pgInt4FromInt(int(admin.ID)))
	if err != nil {
		t.Fatalf("ActivateDisabledUsersByAdminID: %v", err)
	}
	if len(affected) != 1 {
		t.Fatalf("affected %d users, want 1: %+v", len(affected), affected)
	}

	assertStatus := func(id int32, want string) {
		t.Helper()
		u, err := q.GetUserByID(ctx, id)
		if err != nil {
			t.Fatalf("GetUserByID(%d): %v", id, err)
		}
		if u.Status != want {
			t.Errorf("user %d status = %q, want %q", id, u.Status, want)
		}
	}
	assertStatus(disabled.ID, statusActive)
	assertStatus(alreadyActive.ID, statusActive)
	assertStatus(expired.ID, "expired")                  // untouched - not disabled
	assertStatus(otherAdminsDisabled.ID, statusDisabled) // untouched - different admin
}

func TestResetAdminUsageZeroesCounterAndArchivesPriorValue(t *testing.T) {
	pool := testPool(t)
	truncateAdmins(t, pool)
	q := generated.New(pool)
	ctx := context.Background()

	admin, err := q.CreateAdmin(ctx, generated.CreateAdminParams{Username: "reseller", HashedPassword: "h"})
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE admins SET users_usage = 500 WHERE id = $1", admin.ID); err != nil {
		t.Fatalf("seed users_usage: %v", err)
	}

	updated, err := q.ResetAdminUsage(ctx, admin.ID)
	if err != nil {
		t.Fatalf("ResetAdminUsage: %v", err)
	}
	if updated.UsersUsage != 0 {
		t.Errorf("UsersUsage = %d, want 0", updated.UsersUsage)
	}

	var loggedTraffic int64
	if err := pool.QueryRow(ctx, "SELECT used_traffic_at_reset FROM admin_usage_logs WHERE admin_id = $1", admin.ID).Scan(&loggedTraffic); err != nil {
		t.Fatalf("query admin_usage_logs: %v", err)
	}
	if loggedTraffic != 500 {
		t.Errorf("archived used_traffic_at_reset = %d, want 500", loggedTraffic)
	}

	// A second reset on an already-zero counter must not log a second,
	// misleading "reset from 0" row - see ResetAdminUsage's own WHERE clause.
	if _, err := q.ResetAdminUsage(ctx, admin.ID); err != nil {
		t.Fatalf("second ResetAdminUsage: %v", err)
	}
	var logCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM admin_usage_logs WHERE admin_id = $1", admin.ID).Scan(&logCount); err != nil {
		t.Fatalf("count admin_usage_logs: %v", err)
	}
	if logCount != 1 {
		t.Errorf("admin_usage_logs rows = %d, want 1 (second reset-from-zero must not log again)", logCount)
	}
}
