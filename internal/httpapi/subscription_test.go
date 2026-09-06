package httpapi

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/subscription"
)

const testSubSecret = "test-secret" // matches newTestRouter's jwtSecret

func TestSubscriptionV2rayLinks(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "VLESS TCP", "protocol": "vless", "network": "tcp", "security": "tls"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "{USERNAME} - Node", "address": "1.2.3.4", "port": 443, "sni": "example.com", "security": "tls"}},
	})
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "sub_test_user",
		"proxies":  map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}

	subToken := subscription.CreateToken("sub_test_user", []byte(testSubSecret))
	subResp := doRequest(t, router, "GET", "/sub/"+subToken, "", nil)
	if subResp.Code != 200 {
		t.Fatalf("get subscription: %d body=%s", subResp.Code, subResp.Raw)
	}

	decoded, err := base64.StdEncoding.DecodeString(string(subResp.Raw))
	if err != nil {
		t.Fatalf("subscription body is not valid base64: %v", err)
	}
	links := strings.Split(strings.TrimSpace(string(decoded)), "\n")
	if len(links) != 1 || !strings.HasPrefix(links[0], "vless://") {
		t.Fatalf("expected exactly one vless:// link, got: %q", links)
	}
	if !strings.Contains(links[0], "#sub_test_user%20-%20Node") {
		t.Errorf("remark placeholder not substituted correctly: %s", links[0])
	}
}

func TestSubscriptionRejectsRevokedToken(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "revoke_sub_test", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	oldToken := subscription.CreateToken("revoke_sub_test", []byte(testSubSecret))

	// The token's timestamp is minted by this test process's clock; sub_revoked_at
	// is set by Postgres's own now() on a different machine (see the SSH-tunneled
	// test DB in memory) - a couple of seconds of slack keeps a small clock skew
	// between the two from making this test flaky.
	time.Sleep(2 * time.Second)

	revokeResp := doRequest(t, router, "POST", "/api/user/revoke_sub_test/revoke_sub", token, nil)
	if revokeResp.Code != 200 {
		t.Fatalf("revoke sub: %d %v", revokeResp.Code, revokeResp.Body)
	}

	subResp := doRequest(t, router, "GET", "/sub/"+oldToken, "", nil)
	if subResp.Code != 404 {
		t.Fatalf("expected 404 for a token issued before sub_revoked_at, got %d", subResp.Code)
	}
}

func TestSubscriptionSingBoxFormat(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless", "security": "tls"}})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "Node", "address": "1.2.3.4", "port": 443, "sni": "example.com", "security": "tls"}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "singbox_sub_test", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	subToken := subscription.CreateToken("singbox_sub_test", []byte(testSubSecret))
	subResp := doRequest(t, router, "GET", "/sub/"+subToken+"/sing-box", "", nil)
	if subResp.Code != 200 {
		t.Fatalf("get sing-box subscription: %d body=%s", subResp.Code, subResp.Raw)
	}
	if !strings.Contains(string(subResp.Raw), `"type": "vless"`) {
		t.Errorf("sing-box config missing vless outbound: %s", subResp.Raw)
	}
	if !strings.Contains(string(subResp.Raw), `"type": "selector"`) {
		t.Errorf("sing-box config missing selector outbound: %s", subResp.Raw)
	}
}

func TestSubscriptionUnknownTokenRejected(t *testing.T) {
	router, _ := newTestRouter(t)
	resp := doRequest(t, router, "GET", "/sub/not-a-real-token", "", nil)
	if resp.Code != 404 {
		t.Fatalf("expected 404 for garbage token, got %d", resp.Code)
	}
}
