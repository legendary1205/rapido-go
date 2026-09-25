package usagejob

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

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
	if _, err := pool.Exec(context.Background(), "TRUNCATE node_user_usages, users RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

// seedUsage inserts one master-node (node_id NULL) usage row per hour for
// `hours` hours ending `endAgo` before now, for the given user.
func seedUsage(t *testing.T, pool *pgxpool.Pool, userID int32, endAgo time.Duration, hours int, bytesPerRow int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO node_user_usages (created_at, user_id, node_id, used_traffic)
SELECT date_trunc('hour', now() - $2::bigint * interval '1 second') - g * interval '1 hour', $1, NULL, $3
FROM generate_series(0, $4::int - 1) g`, userID, int64(endAgo.Seconds()), bytesPerRow, hours)
	if err != nil {
		t.Fatalf("seed usage: %v", err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM node_user_usages").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestSweepDeletesOnlyRowsOlderThanCutoffInBatches(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := generated.New(pool)

	var userID int32
	if err := pool.QueryRow(ctx, "INSERT INTO users (username, used_traffic) VALUES ('retention-user', 12345) RETURNING id").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 10 days of rows that stay (0..10d old) and 100 days of rows that go
	// (older than 30d), with a gap in between that proves the cutoff, not the
	// row count, decides.
	seedUsage(t, pool, userID, 0, 24*10, 1000)
	seedUsage(t, pool, userID, 31*24*time.Hour, 24*100, 1000)
	kept, total := 24*10, 24*10+24*100
	if got := countRows(t, pool); got != total {
		t.Fatalf("seeded %d rows, want %d", got, total)
	}

	// 2400 old rows in batches of 500: four full statements plus a partial one.
	removed, err := Sweep(ctx, q, time.Now().Add(-30*24*time.Hour), 500, time.Millisecond)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != int64(total-kept) {
		t.Errorf("removed = %d, want %d", removed, total-kept)
	}
	if got := countRows(t, pool); got != kept {
		t.Errorf("rows left = %d, want %d", got, kept)
	}

	// A second sweep with nothing left is a cheap no-op.
	if removed, err := Sweep(ctx, q, time.Now().Add(-30*24*time.Hour), 500, time.Millisecond); err != nil || removed != 0 {
		t.Errorf("second Sweep = (%d, %v), want (0, nil)", removed, err)
	}

	// Totals come from the counter on users, and the windowed per-node sum for
	// the default 30-day window still sees every surviving row.
	var used int64
	if err := pool.QueryRow(ctx, "SELECT used_traffic FROM users WHERE id = $1", userID).Scan(&used); err != nil {
		t.Fatalf("read used_traffic: %v", err)
	}
	if used != 12345 {
		t.Errorf("users.used_traffic = %d after retention, want 12345 untouched", used)
	}
	sums, err := q.SumUserUsageByNode(ctx, generated.SumUserUsageByNodeParams{
		UserID:      pgtype.Int4{Int32: userID, Valid: true},
		CreatedAt:   pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, -30), Valid: true},
		CreatedAt_2: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("SumUserUsageByNode: %v", err)
	}
	if len(sums) != 1 || sums[0].Total != int64(kept)*1000 {
		t.Errorf("30-day window sum = %+v, want one row totalling %d", sums, kept*1000)
	}
}

type failingQueries struct{ calls int }

func (f *failingQueries) DeleteOldNodeUserUsages(context.Context, generated.DeleteOldNodeUserUsagesParams) (int64, error) {
	f.calls++
	if f.calls == 1 {
		return 5, nil
	}
	return 0, errors.New("boom")
}

func TestSweepReportsRowsRemovedBeforeAnError(t *testing.T) {
	f := &failingQueries{}
	removed, err := Sweep(context.Background(), f, time.Now(), 5, time.Millisecond)
	if err == nil {
		t.Fatal("Sweep returned nil error, want the delete failure")
	}
	if removed != 5 {
		t.Errorf("removed = %d, want the 5 deleted before the failure", removed)
	}
}

func TestSweepStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	full := &fullBatchQueries{}
	_, err := Sweep(ctx, full, time.Now(), 3, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Sweep err = %v, want context.Canceled", err)
	}
	if full.calls != 1 {
		t.Errorf("delete calls = %d, want 1 (canceled before the pause between batches)", full.calls)
	}
}

type fullBatchQueries struct{ calls int }

func (f *fullBatchQueries) DeleteOldNodeUserUsages(_ context.Context, arg generated.DeleteOldNodeUserUsagesParams) (int64, error) {
	f.calls++
	return int64(arg.BatchSize), nil
}

func TestRunReturnsImmediatelyWhenRetentionIsZero(t *testing.T) {
	done := make(chan struct{})
	go func() {
		Run(context.Background(), &fullBatchQueries{}, nil, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run with retention 0 did not return, want the job disabled")
	}
}
