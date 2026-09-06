package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/legendary1205/rapido-go/internal/subscription"
)

// TestSubscriptionPageServedForHTMLAccept proves the real content
// negotiation branch (Accept-header based, not User-Agent) actually wires
// through to a real rendered page - not just that the embedded template
// parses (already proven at package init by every other test in this
// binary even running at all).
func TestSubscriptionPageServedForHTMLAccept(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "VLESS TCP", "protocol": "vless", "network": "tcp", "security": "tls"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "Node", "address": "1.2.3.4", "port": 443, "sni": "example.com", "security": "tls"}},
	})
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "html_page_test_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}

	subToken := subscription.CreateToken("html_page_test_user", []byte(testSubSecret))
	req := httptest.NewRequest(http.MethodGet, "/sub/"+subToken, nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an HTML Accept header, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("missing/wrong CSP header: %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", rec.Header().Get("X-Frame-Options"))
	}

	body := rec.Body.String()
	if !strings.Contains(body, "html_page_test_user") {
		t.Error("rendered page does not contain the username anywhere")
	}
	if !strings.Contains(body, "window.__RAPIDO__") {
		t.Error("rendered page is missing the window.__RAPIDO__ data blob")
	}
	if !strings.Contains(body, `"status":"active"`) {
		t.Errorf("rendered page's data blob missing expected status field: looking in body of length %d", len(body))
	}
	if !strings.Contains(body, "1.2.3.4") {
		t.Error("rendered page's embedded links don't contain the configured host address - buildUserLinks wiring broken")
	}
}

// TestSubscriptionRawConfigStillServedForNonHTMLAccept proves the new
// Accept-header branch didn't break the existing raw-config delivery path
// real VPN client apps depend on.
func TestSubscriptionRawConfigStillServedForNonHTMLAccept(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "raw_config_still_works_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	subToken := subscription.CreateToken("raw_config_still_works_user", []byte(testSubSecret))

	req := httptest.NewRequest(http.MethodGet, "/sub/"+subToken, nil)
	req.Header.Set("Accept", "*/*")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a non-HTML Accept header, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "window.__RAPIDO__") {
		t.Error("a non-HTML Accept request got the HTML page instead of raw subscription content")
	}
}

// TestSubscriptionPageDoesNotUpdateSubUserAgent mirrors the Python system's
// own behavior: loading the browser page must not bump
// sub_updated_at/sub_last_user_agent (those only track real VPN client
// fetches), unlike GET /sub/:token's raw-config branch. Verified directly
// against the column, not just "the request didn't crash".
func TestSubscriptionPageDoesNotUpdateSubUserAgent(t *testing.T) {
	router, token := newTestRouter(t)
	pool := testPool(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "html_no_useragent_bump_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	subToken := subscription.CreateToken("html_no_useragent_bump_user", []byte(testSubSecret))

	req := httptest.NewRequest(http.MethodGet, "/sub/"+subToken, nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "some-real-browser/1.0")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get html page: %d", rec.Code)
	}

	var lastUserAgent *string
	if err := pool.QueryRow(context.Background(),
		"SELECT sub_last_user_agent FROM users WHERE username = $1", "html_no_useragent_bump_user",
	).Scan(&lastUserAgent); err != nil {
		t.Fatalf("query sub_last_user_agent: %v", err)
	}
	if lastUserAgent != nil {
		t.Errorf("sub_last_user_agent = %q, want NULL - loading the HTML page must not record it (see recordSubUserAgent's callers)", *lastUserAgent)
	}

	// Sanity check the opposite: a real raw-config hit (any non-HTML
	// Accept) DOES record it, proving this isn't just a broken/no-op write
	// path in general.
	req = httptest.NewRequest(http.MethodGet, "/sub/"+subToken, nil)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "v2rayNG/1.8.0")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get raw config: %d", rec.Code)
	}
	if err := pool.QueryRow(context.Background(),
		"SELECT sub_last_user_agent FROM users WHERE username = $1", "html_no_useragent_bump_user",
	).Scan(&lastUserAgent); err != nil {
		t.Fatalf("query sub_last_user_agent: %v", err)
	}
	if lastUserAgent == nil || *lastUserAgent != "v2rayNG/1.8.0" {
		t.Errorf("sub_last_user_agent = %v, want \"v2rayNG/1.8.0\" after a real raw-config fetch", lastUserAgent)
	}
}
