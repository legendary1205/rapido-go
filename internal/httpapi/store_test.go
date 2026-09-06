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
func testPool(t *testing.T) *pgxpool.Pool {
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
