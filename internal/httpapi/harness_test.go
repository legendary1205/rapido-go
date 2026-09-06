package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/certs"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

const (
	testSudoUsername = "test-sudo"
	testSudoPassword = "test-sudo-password"
)

// newTestRouter builds a full router (real Postgres, real JWT issuer) and
// returns it plus a bearer token for the env-bootstrapped sudo account.
// Skips if TEST_DATABASE_URL isn't set - see store_test.go's testPool.
func newTestRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	pool := testPool(t)
	truncateAll(t, pool)

	store := NewStore(pool)
	ensureTestCA(t, store)
	testSecret := []byte("test-secret")
	issuer := auth.NewTokenIssuer(testSecret, time.Hour)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewHandler(store, issuer, testSudoUsername, testSudoPassword, testSecret, "203.0.113.1", "", logger)
	router := NewRouter(handler, logger, []string{"*"})

	token, err := issuer.Issue(testSudoUsername, true)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return router, token
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
		"TRUNCATE admin_usage_logs, users, admins, inbounds, hosts, user_templates, nodes RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("truncate: %v", err)
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
