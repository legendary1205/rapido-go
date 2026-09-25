package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// countBcrypt swaps the handler's login verifier for one that still does the
// real bcrypt comparison but counts how often it ran.
func countBcrypt(h *Handler) *atomic.Int32 {
	var calls atomic.Int32
	h.loginVerifier = auth.NewVerifyCacheWith(func(plain, hashed string) bool {
		calls.Add(1)
		return auth.VerifyPassword(plain, hashed)
	})
	return &calls
}

func loginAttempt(router http.Handler, ip, username, password string) *httptest.ResponseRecorder {
	form := url.Values{"username": {username}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", ip)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func createLoginAdmin(t *testing.T, h *Handler, username, password string) {
	t.Helper()
	hashed, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := h.store.Queries.CreateAdmin(context.Background(), generated.CreateAdminParams{Username: username, HashedPassword: hashed}); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
}

func TestLoginCorrectPasswordSkipsBcryptOnRepeat(t *testing.T) {
	router, _, h := newTestRouterAndHandler(t)
	calls := countBcrypt(h)
	createLoginAdmin(t, h, "cache-reseller", "right-password")

	for i := 0; i < 4; i++ {
		rec := loginAttempt(router, "203.0.113.10", "cache-reseller", "right-password")
		if rec.Code != http.StatusOK {
			t.Fatalf("login #%d = %d, want 200: %s", i+1, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"access_token"`) || !strings.Contains(rec.Body.String(), `"token_type":"bearer"`) {
			t.Fatalf("login #%d body = %s, want the usual token response", i+1, rec.Body.String())
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("bcrypt ran %d times for 4 identical correct logins, want 1", got)
	}
}

func TestLoginPasswordChangeInvalidatesCachedVerdict(t *testing.T) {
	router, _, h := newTestRouterAndHandler(t)
	calls := countBcrypt(h)
	createLoginAdmin(t, h, "cache-changer", "old-password")

	if rec := loginAttempt(router, "203.0.113.11", "cache-changer", "old-password"); rec.Code != http.StatusOK {
		t.Fatalf("login with the original password = %d, want 200", rec.Code)
	}

	newHash, err := auth.HashPassword("new-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := h.store.Pool.Exec(context.Background(), "UPDATE admins SET hashed_password = $2 WHERE username = $1", "cache-changer", newHash); err != nil {
		t.Fatalf("change password: %v", err)
	}

	if rec := loginAttempt(router, "203.0.113.11", "cache-changer", "old-password"); rec.Code != http.StatusUnauthorized {
		t.Errorf("login with the replaced password = %d, want 401 (the cached success must not outlive the hash)", rec.Code)
	}
	if rec := loginAttempt(router, "203.0.113.11", "cache-changer", "new-password"); rec.Code != http.StatusOK {
		t.Errorf("login with the new password = %d, want 200", rec.Code)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("bcrypt ran %d times, want 3 (original, replaced-as-wrong, new)", got)
	}
}

// A client hammering one wrong password must cost a single bcrypt, yet every
// attempt still counts toward the per-IP limiter and is answered exactly like
// an uncached failure.
func TestLoginRepeatedWrongPasswordIsCachedButStillRateLimited(t *testing.T) {
	router, _, h := newTestRouterAndHandler(t)
	calls := countBcrypt(h)
	createLoginAdmin(t, h, "cache-victim", "right-password")

	const ip = "203.0.113.12"
	var firstBody string
	for i := 0; i < loginRateLimitMax; i++ {
		rec := loginAttempt(router, ip, "cache-victim", "wrong-password")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong attempt #%d = %d, want 401", i+1, rec.Code)
		}
		if i == 0 {
			firstBody = rec.Body.String()
		} else if rec.Body.String() != firstBody {
			t.Fatalf("cached failure body %q differs from the first %q", rec.Body.String(), firstBody)
		}
	}
	if !strings.Contains(firstBody, "Incorrect username or password") {
		t.Errorf("failure body = %s, want the standard detail", firstBody)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("bcrypt ran %d times for %d identical wrong logins, want 1", got, loginRateLimitMax)
	}
	if rec := loginAttempt(router, ip, "cache-victim", "wrong-password"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("attempt past the limit = %d, want 429 (cache hits must still count as failures)", rec.Code)
	}

	// A different wrong password, from another address, is a fresh bcrypt, and
	// the right password is still accepted.
	if rec := loginAttempt(router, "203.0.113.13", "cache-victim", "another-typo"); rec.Code != http.StatusUnauthorized {
		t.Errorf("different wrong password = %d, want 401", rec.Code)
	}
	if rec := loginAttempt(router, "203.0.113.13", "cache-victim", "right-password"); rec.Code != http.StatusOK {
		t.Errorf("correct password after a wrong one = %d, want 200", rec.Code)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("bcrypt ran %d times, want 3 (first wrong, second wrong, correct)", got)
	}
}
