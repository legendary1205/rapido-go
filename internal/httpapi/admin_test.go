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
// motivated adding this - see tooManyFailedLogins's own doc comment).
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

// TestSuccessfulLoginsAreNeverRateLimited is the exact scenario a live
// compatibility test against a real, unmodified reseller bot (WizWiz)
// surfaced: it re-authenticates fresh on nearly every single API call
// rather than caching a token, so a legitimate burst of correct-credential
// logins must never trip the limiter meant for wrong-credential spam.
func TestSuccessfulLoginsAreNeverRateLimited(t *testing.T) {
	router, _ := newTestRouter(t)

	const testIP = "203.0.113.99"
	form := url.Values{"username": {testSudoUsername}, "password": {testSudoPassword}}

	for i := 0; i < loginRateLimitMax*2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", testIP)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("correct-credential login #%d got %d, want 200 - a legitimate high-frequency bot must never be rate-limited", i+1, rec.Code)
		}
	}
}

// TestSuccessfulLoginClearsFailedAttemptCounter proves a real admin who
// mistypes their password a few times isn't left sitting close to the
// limit for the rest of the window once they get it right.
func TestSuccessfulLoginClearsFailedAttemptCounter(t *testing.T) {
	router, _ := newTestRouter(t)

	const testIP = "203.0.113.100"
	wrongForm := url.Values{"username": {testSudoUsername}, "password": {"wrong"}}
	rightForm := url.Values{"username": {testSudoUsername}, "password": {testSudoPassword}}

	for i := 0; i < loginRateLimitMax-1; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(wrongForm.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", testIP)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong-password attempt %d got %d, want 401", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(rightForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", testIP)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct password after near-limit failures got %d, want 200", rec.Code)
	}

	// The counter should be cleared now - another wrong attempt right after
	// a success must not immediately hit the limit.
	req = httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(wrongForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", testIP)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong attempt right after a success got %d, want 401 (not 429 - the counter should have been cleared)", rec.Code)
	}
}
