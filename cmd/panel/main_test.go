package main

import (
	"context"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// testPool mirrors internal/httpapi/store_test.go's own helper - skips
// instead of failing when TEST_DATABASE_URL isn't set, so `go test ./...`
// still runs everywhere else.
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

// TestEnsureJWTSecretReturnsTheOriginal32DecodedBytes is the regression test
// for a real bug found via a 30k-user load test: ensureJWTSecret used to
// return []byte(secretHexString) - the hex STRING's own 64 ASCII bytes -
// instead of decoding it back to the 32 random bytes hex.EncodeToString
// encoded in the first place. Harmless to the running panel itself (every
// process derived the same wrong value from the same row, so sign/verify
// always agreed with each other), but meant an external tool reading the
// stored hex value and correctly hex-decoding it - exactly what the column's
// own name and format promise - could never reconstruct a valid signature.
func TestEnsureJWTSecretReturnsTheOriginal32DecodedBytes(t *testing.T) {
	pool := testPool(t)
	if _, err := pool.Exec(context.Background(), "TRUNCATE jwt_secrets RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate jwt_secrets: %v", err)
	}
	q := generated.New(pool)
	ctx := context.Background()

	secret, err := ensureJWTSecret(ctx, q)
	if err != nil {
		t.Fatalf("ensureJWTSecret (create path): %v", err)
	}
	if len(secret) != 32 {
		t.Errorf("len(secret) = %d, want 32 (the documented random-byte length, not 64 ASCII-hex-digit bytes)", len(secret))
	}

	row, err := q.GetJWTSecret(ctx)
	if err != nil {
		t.Fatalf("GetJWTSecret: %v", err)
	}
	stored, err := hex.DecodeString(row.SecretKey)
	if err != nil {
		t.Fatalf("stored secret_key is not valid hex: %v", err)
	}
	if hex.EncodeToString(secret) != hex.EncodeToString(stored) {
		t.Errorf("ensureJWTSecret's returned bytes don't match hex-decoding the stored column directly")
	}

	// A second call (simulating a process restart) must read the SAME row
	// back and decode it the SAME way, not regenerate a new one.
	again, err := ensureJWTSecret(ctx, q)
	if err != nil {
		t.Fatalf("ensureJWTSecret (read path): %v", err)
	}
	if hex.EncodeToString(again) != hex.EncodeToString(secret) {
		t.Errorf("second ensureJWTSecret call returned a different secret than the first - not idempotent across a restart")
	}
}
