package httpapi

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/subscription"
)

// TestAdminCacheInvalidatedOnSudoPromotion proves that CachedGetAdminByUsername
// (used by the auth middleware's ResolveAdmin on nearly every request) is
// invalidated on write - not just left to the 15-minute safety-net TTL. If
// InvalidateAdmin were missing from handleUpdateAdmin, the still-cached
// pre-promotion row would keep this same token locked out of sudo-only
// routes for up to 15 minutes after the promotion actually took effect.
func TestAdminCacheInvalidatedOnSudoPromotion(t *testing.T) {
	router, sudoToken := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{
		"username": "promote_test", "password": "pw12345", "is_sudo": false,
	})
	if resp.Code != 200 {
		t.Fatalf("create admin: %d %v", resp.Code, resp.Body)
	}
	token := loginAs(t, router, "promote_test", "pw12345")

	// Warm the cache with the pre-promotion (non-sudo) row.
	resp = doRequest(t, router, "GET", "/api/admins", token, nil)
	if resp.Code != 403 {
		t.Fatalf("expected 403 before promotion, got %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "PUT", "/api/admin/promote_test", sudoToken, map[string]interface{}{"is_sudo": true})
	if resp.Code != 200 {
		t.Fatalf("promote to sudo: %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "GET", "/api/admins", token, nil)
	if resp.Code != 200 {
		t.Fatalf("expected 200 immediately after promotion (cache should be invalidated, not stale for 15 minutes), got %d %v", resp.Code, resp.Body)
	}
}

// TestAdminCacheInvalidatedOnPasswordReset proves the same thing for the
// security-sensitive password_reset_at field: a token issued before a
// password reset must be rejected on the very next request, not up to 15
// minutes later because a stale cached admin row still carries the old
// (or null) password_reset_at.
func TestAdminCacheInvalidatedOnPasswordReset(t *testing.T) {
	router, sudoToken := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{
		"username": "resetpw_test", "password": "pw12345", "is_sudo": false,
	})
	if resp.Code != 200 {
		t.Fatalf("create admin: %d %v", resp.Code, resp.Body)
	}
	oldToken := loginAs(t, router, "resetpw_test", "pw12345")

	// Warm the cache with the pre-reset row, and give the reset a clock
	// edge to land after the token's issued-at second (see
	// TestSubscriptionRejectsRevokedToken for the same clock-skew concern).
	doRequest(t, router, "GET", "/api/admin", oldToken, nil)
	time.Sleep(2 * time.Second)

	resp = doRequest(t, router, "PUT", "/api/admin/resetpw_test", sudoToken, map[string]interface{}{"password": "newpassword456"})
	if resp.Code != 200 {
		t.Fatalf("reset password: %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "GET", "/api/admin", oldToken, nil)
	if resp.Code != 401 {
		t.Fatalf("expected 401 for a token issued before password_reset_at (cache should be invalidated, not stale for 15 minutes), got %d %v", resp.Code, resp.Body)
	}
}

// TestSubscriptionReflectsHostReplaceAfterInvalidation proves
// CachedListHostsByInboundTag is invalidated on PUT /api/hosts - not just
// left to the 6-hour safety-net TTL, which would otherwise keep serving a
// user's already-warmed subscription the pre-replace host indefinitely.
func TestSubscriptionReflectsHostReplaceAfterInvalidation(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "VLESS TCP", "protocol": "vless", "network": "tcp", "security": "tls"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "Node", "address": "1.2.3.4", "port": 443, "sni": "example.com", "security": "tls"}},
	})
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "host_invalidation_test", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}
	subToken := subscription.CreateToken("host_invalidation_test", []byte(testSubSecret))

	// Warm the host cache for "VLESS TCP" with the old address.
	subResp := doRequest(t, router, "GET", "/sub/"+subToken, "", nil)
	if subResp.Code != 200 {
		t.Fatalf("get subscription: %d body=%s", subResp.Code, subResp.Raw)
	}
	if !subContainsPlain(t, subResp.Raw, "1.2.3.4") {
		t.Fatalf("expected initial subscription to contain the original host address 1.2.3.4, got: %s", subResp.Raw)
	}

	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "Node", "address": "5.6.7.8", "port": 443, "sni": "example.com", "security": "tls"}},
	})

	subResp = doRequest(t, router, "GET", "/sub/"+subToken, "", nil)
	if subResp.Code != 200 {
		t.Fatalf("get subscription after host replace: %d body=%s", subResp.Code, subResp.Raw)
	}
	if subContainsPlain(t, subResp.Raw, "1.2.3.4") {
		t.Errorf("subscription still contains the replaced host address 1.2.3.4 (host cache should be invalidated, not stale for 6 hours): %s", subResp.Raw)
	}
	if !subContainsPlain(t, subResp.Raw, "5.6.7.8") {
		t.Errorf("expected subscription to contain the new host address 5.6.7.8, got: %s", subResp.Raw)
	}
}

// TestSubscriptionReflectsExcludedInboundChangeAfterInvalidation proves
// CachedListExcludedInboundTags is invalidated on PUT /api/user's inbounds
// reconciliation - not just left to the 24-hour safety-net TTL, which would
// otherwise keep serving a link for a tag the admin just excluded.
func TestSubscriptionReflectsExcludedInboundChangeAfterInvalidation(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "VLESS TCP", "protocol": "vless", "network": "tcp", "security": "tls"},
		{"tag": "VLESS WS", "protocol": "vless", "network": "ws", "security": "tls"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "TCP-Node", "address": "1.1.1.1", "port": 443, "sni": "example.com", "security": "tls"}},
		"VLESS WS":  []map[string]interface{}{{"remark": "WS-Node", "address": "2.2.2.2", "port": 8443, "sni": "example.com", "security": "tls"}},
	})
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "excl_invalidation_test", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}
	subToken := subscription.CreateToken("excl_invalidation_test", []byte(testSubSecret))

	// Warm the excluded-inbounds cache (empty set: both tags included).
	subResp := doRequest(t, router, "GET", "/sub/"+subToken, "", nil)
	if subResp.Code != 200 {
		t.Fatalf("get subscription: %d body=%s", subResp.Code, subResp.Raw)
	}
	if !subContainsPlain(t, subResp.Raw, "2.2.2.2") {
		t.Fatalf("expected initial subscription to include the VLESS WS link (2.2.2.2), got: %s", subResp.Raw)
	}

	resp = doRequest(t, router, "PUT", "/api/user/excl_invalidation_test", token, map[string]interface{}{
		"inbounds": map[string]interface{}{"vless": []string{"VLESS TCP"}},
	})
	if resp.Code != 200 {
		t.Fatalf("exclude VLESS WS: %d %v", resp.Code, resp.Body)
	}

	subResp = doRequest(t, router, "GET", "/sub/"+subToken, "", nil)
	if subResp.Code != 200 {
		t.Fatalf("get subscription after exclusion: %d body=%s", subResp.Code, subResp.Raw)
	}
	if subContainsPlain(t, subResp.Raw, "2.2.2.2") {
		t.Errorf("subscription still includes the now-excluded VLESS WS link (excluded-inbounds cache should be invalidated, not stale for 24 hours): %s", subResp.Raw)
	}
	if !subContainsPlain(t, subResp.Raw, "1.1.1.1") {
		t.Errorf("expected subscription to still include the VLESS TCP link, got: %s", subResp.Raw)
	}
}

// subContainsPlain base64-decodes a /sub/:token response body and checks
// the decoded v2ray-links text for a substring.
func subContainsPlain(t *testing.T, raw []byte, substr string) bool {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatalf("subscription body is not valid base64: %v", err)
	}
	return strings.Contains(string(decoded), substr)
}
