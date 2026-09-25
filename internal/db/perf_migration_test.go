package db_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// hotUserQueries are every query in internal/db/queries that filters or
// orders on users.online_at or users.expire, copied verbatim (with $n
// parameters) from the generated code so this test can prove dropping the two
// indexes in migration 00016 changes no result. If one of those queries
// changes, update the copy here.
var hotUserQueries = []struct {
	name string
	sql  string
	args func(now time.Time) []any
}{
	{
		name: "CountOnlineUsersSince",
		sql: `SELECT count(*) FROM users
WHERE online_at >= $1::timestamptz AND ($2::int IS NULL OR admin_id = $2::int)`,
		args: func(now time.Time) []any { return []any{now.Add(-180 * time.Second), nil} },
	},
	{
		name: "GetUsersNeedingStatusReview",
		sql: `SELECT id FROM users
WHERE status = 'active'
  AND ((data_limit IS NOT NULL AND data_limit > 0 AND used_traffic >= data_limit)
    OR (expire IS NOT NULL AND expire != 0 AND expire <= $1))`,
		args: func(now time.Time) []any { return []any{int32(now.Unix())} },
	},
	{
		name: "ListExpiredUsers",
		sql: `SELECT id FROM users
WHERE status IN ('expired', 'limited') AND expire IS NOT NULL AND expire >= $1 AND expire <= $2
ORDER BY id`,
		args: func(now time.Time) []any {
			return []any{int32(now.Add(-30 * 24 * time.Hour).Unix()), int32(now.Unix())}
		},
	},
	{
		name: "ListUsers sort=expire",
		sql: `SELECT id FROM users
WHERE ($1::int IS NULL OR admin_id = $1::int)
ORDER BY CASE WHEN $2::text = 'expire' THEN expire END ASC, id DESC
LIMIT 50`,
		args: func(now time.Time) []any { return []any{nil, "expire"} },
	},
}

func perfTestPool(t *testing.T) *pgxpool.Pool {
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

// migrationSection returns the Up or Down half of a goose file.
func migrationSection(t *testing.T, file, section string) string {
	t.Helper()
	raw, err := os.ReadFile("migrations/" + file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	up, down, ok := strings.Cut(string(raw), "-- +goose Down")
	if !ok {
		t.Fatalf("%s has no Down section", file)
	}
	if section == "up" {
		return up
	}
	return down
}

func indexExists(t *testing.T, ctx context.Context, tx pgx.Tx, name string) bool {
	t.Helper()
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1", name).Scan(&n); err != nil {
		t.Fatalf("pg_indexes %s: %v", name, err)
	}
	return n == 1
}

func reloptions(t *testing.T, ctx context.Context, tx pgx.Tx, table string) []string {
	t.Helper()
	var opts []string
	if err := tx.QueryRow(ctx, "SELECT COALESCE(reloptions, '{}') FROM pg_class WHERE oid = $1::regclass", table).Scan(&opts); err != nil {
		t.Fatalf("reloptions %s: %v", table, err)
	}
	return opts
}

func hasOption(opts []string, want string) bool {
	for _, o := range opts {
		if o == want {
			return true
		}
	}
	return false
}

// countRows runs a query and returns how many rows it yields.
func countRows(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args []any) int {
	t.Helper()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("query: %v\n%s", err, sql)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return n
}

// explain returns the EXPLAIN (ANALYZE) plan text of a query.
func explain(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args []any) string {
	t.Helper()
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+sql, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, sql)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

// TestPerfIndexesMigration applies 00016's Up and Down inside a transaction
// that is always rolled back, against 10k seeded users, so it never leaves
// the shared test database changed. It proves: the two indexes are dropped,
// the storage parameters are set, every query touching the dropped columns
// returns the same rows without the indexes, and Down restores everything.
func TestPerfIndexesMigration(t *testing.T) {
	pool := perfTestPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	// A fresh database has the indexes from 00001; skip if a previous run of
	// this migration already applied them for real (dbmigrate up), where the
	// "before" state cannot be rebuilt without also testing Down first.
	if !indexExists(t, ctx, tx, "users_online_at_idx") {
		if _, err := tx.Exec(ctx, migrationSection(t, "00016_perf_indexes.sql", "down")); err != nil {
			t.Fatalf("Down (to restore the pre-migration state): %v", err)
		}
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO admins (username, hashed_password)
SELECT 'perfseed_admin_' || g, 'x' FROM generate_series(1, 5) g`); err != nil {
		t.Fatalf("seed admins: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO users (username, status, used_traffic, data_limit, expire, admin_id, online_at)
SELECT 'perfseed_' || g,
       (ARRAY['active','active','active','active','limited','expired','on_hold','disabled'])[1 + g % 8],
       (random() * 1e9)::bigint,
       CASE WHEN g % 3 = 0 THEN (random() * 2e9)::bigint END,
       CASE WHEN g % 10 = 0 THEN NULL ELSE extract(epoch FROM now())::int + (g % 400 - 200) * 86400 END,
       (SELECT id FROM admins WHERE username = 'perfseed_admin_' || (1 + g % 5)),
       CASE WHEN g % 20 = 0 THEN now() - (g % 60) * interval '1 second' WHEN g % 3 = 0 THEN now() - interval '3 days' END
FROM generate_series(1, 10000) g`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := tx.Exec(ctx, "ANALYZE users"); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	now := time.Now()
	before := map[string]int{}
	beforePlan := map[string]string{}
	for _, q := range hotUserQueries {
		before[q.name] = countRows(t, ctx, tx, q.sql, q.args(now))
		beforePlan[q.name] = explain(t, ctx, tx, q.sql, q.args(now))
	}
	if before["CountOnlineUsersSince"] != 1 {
		t.Fatalf("CountOnlineUsersSince returned %d rows, want the single count row", before["CountOnlineUsersSince"])
	}

	if _, err := tx.Exec(ctx, migrationSection(t, "00016_perf_indexes.sql", "up")); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, idx := range []string{"users_online_at_idx", "users_expire_idx"} {
		if indexExists(t, ctx, tx, idx) {
			t.Errorf("index %s still exists after Up", idx)
		}
	}
	for _, want := range []string{"fillfactor=85", "autovacuum_vacuum_scale_factor=0.02", "autovacuum_analyze_scale_factor=0.02"} {
		if !hasOption(reloptions(t, ctx, tx, "users"), want) {
			t.Errorf("users reloptions %v missing %s", reloptions(t, ctx, tx, "users"), want)
		}
	}
	for _, want := range []string{"fillfactor=90", "autovacuum_vacuum_scale_factor=0.02", "autovacuum_analyze_scale_factor=0.02"} {
		if !hasOption(reloptions(t, ctx, tx, "node_user_usages"), want) {
			t.Errorf("node_user_usages reloptions %v missing %s", reloptions(t, ctx, tx, "node_user_usages"), want)
		}
	}
	// A second Up must be harmless (IF EXISTS), so a re-run never errors.
	if _, err := tx.Exec(ctx, migrationSection(t, "00016_perf_indexes.sql", "up")); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	for _, q := range hotUserQueries {
		got := countRows(t, ctx, tx, q.sql, q.args(now))
		if got != before[q.name] {
			t.Errorf("%s returned %d rows after dropping the indexes, %d before", q.name, got, before[q.name])
		}
		plan := explain(t, ctx, tx, q.sql, q.args(now))
		t.Logf("%s\n--- plan before (indexes present):\n%s\n--- plan after (indexes dropped):\n%s", q.name, beforePlan[q.name], plan)
		if strings.Contains(plan, "users_online_at_idx") || strings.Contains(plan, "users_expire_idx") {
			t.Errorf("%s plan references a dropped index:\n%s", q.name, plan)
		}
	}

	if _, err := tx.Exec(ctx, migrationSection(t, "00016_perf_indexes.sql", "down")); err != nil {
		t.Fatalf("Down: %v", err)
	}
	for _, idx := range []string{"users_online_at_idx", "users_expire_idx"} {
		if !indexExists(t, ctx, tx, idx) {
			t.Errorf("index %s missing after Down", idx)
		}
	}
	for _, table := range []string{"users", "node_user_usages"} {
		if opts := reloptions(t, ctx, tx, table); len(opts) != 0 {
			t.Errorf("%s reloptions %v not cleared by Down", table, opts)
		}
	}
}

// TestPerfReportSQLRunsReadOnly executes every statement scripts/perf-report.sh
// sends to psql, in a read-only transaction, so the report cannot silently
// break when a table or a Postgres view it reads changes.
func TestPerfReportSQLRunsReadOnly(t *testing.T) {
	pool := perfTestPool(t)
	ctx := context.Background()

	script, err := os.ReadFile("../../scripts/perf-report.sh")
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	statements := regexp.MustCompile(`(?m)^psql "([^"]*)"`).FindAllStringSubmatch(string(script), -1)
	if len(statements) < 6 {
		t.Fatalf("found %d psql statements in the script, want at least 6", len(statements))
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin read-only: %v", err)
	}
	defer tx.Rollback(ctx)
	for _, m := range statements {
		rows, err := tx.Query(ctx, m[1])
		if err != nil {
			t.Fatalf("statement failed: %v\n%s", err, m[1])
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("statement failed: %v\n%s", err, m[1])
		}
		rows.Close()
	}
}
