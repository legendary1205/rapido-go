package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestLoginIsRateLimitedPerIP proves the fix for a real, previously-open
// gap: neither this panel nor the real Python original ever bounded
// POST /api/admin/token per source IP, letting a misbehaving or
// brute-forcing client hammer it indefinitely (the exact pattern that
// motivated adding this - see loginRateLimited's own doc comment).
func TestLoginIsRateLimitedPerIP(t *testing.T) {
	router, _ := newTestRouter(t)

	const testIP = "203.0.113.77" // RFC 5737 TEST-NET-3, never a real client
	form := url.Values{"username": {"nonexistent-admin"}, "password": {"wrong"}}

	var lastCode int
	for i := 0; i < loginRateLimitMax+1; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", testIP)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		lastCode = rec.Code
		if i < loginRateLimitMax && rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401 (bad credentials, not yet rate-limited), got %d", i+1, rec.Code)
		}
	}
	if lastCode != http.StatusTooManyRequests {
		t.Errorf("attempt %d (past the %d/window limit) = %d, want 429", loginRateLimitMax+1, loginRateLimitMax, lastCode)
	}

	// A different source IP has its own independent counter - the first
	// IP's exhausted limit must never bleed into another client's budget.
	req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "203.0.113.88")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a different IP got %d, want 401 (its own limit is untouched by the first IP)", rec.Code)
	}
}
