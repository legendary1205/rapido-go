package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/auth"
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
	issuer := auth.NewTokenIssuer([]byte("test-secret"), time.Hour)
	handler := NewHandler(store, issuer, testSudoUsername, testSudoPassword)
	router := NewRouter(handler, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), []string{"*"})

	token, err := issuer.Issue(testSudoUsername, true)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return router, token
}

// truncateAll clears every table Phase 2 tests touch, so each test starts
// from a clean slate regardless of what earlier tests (or the deployed
// panel sharing this same database over the SSH tunnel) left behind.
func truncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"TRUNCATE admin_usage_logs, users, admins, inbounds, hosts, user_templates RESTART IDENTITY CASCADE")
	if err != nil {
		t.Fatalf("truncate: %v", err)
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
