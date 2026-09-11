package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestSubscriptionOrdersHostsAcrossMultipleInboundTagsByPriority is the
// real regression test for forEachUserHost's bulk-query rewrite
// (ListHostsByInboundTags/ListInboundsByTags replacing a per-tag/per-host
// cache loop plus a Go-side sort.Slice - confirmed via a 30k-user load test
// to be the dominant cost of a single-user GET or subscription fetch,
// ~140ms). The old code gathered hosts across every included tag and
// explicitly re-sorted the combined set by (priority, id); the new bulk
// query does that ordering in SQL instead - this proves a lower-priority
// host on an alphabetically-LATER tag still sorts before a higher-priority
// number... only lower priority values come first, so this places tag B's
// host ahead of tag A's despite tag A sorting first alphabetically.
func TestSubscriptionOrdersHostsAcrossMultipleInboundTagsByPriority(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "A Tag", "protocol": "vless", "network": "tcp", "security": "none"},
		{"tag": "B Tag", "protocol": "vless", "network": "tcp", "security": "none"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"A Tag": []map[string]interface{}{{"remark": "HostA", "address": "1.1.1.1", "port": 443, "security": "none", "priority": 2}},
		"B Tag": []map[string]interface{}{{"remark": "HostB", "address": "2.2.2.2", "port": 443, "security": "none", "priority": 1}},
	})
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "priority_order_user",
		"proxies":  map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}

	subToken := subscription.CreateToken("priority_order_user", []byte(testSubSecret))
	subResp := doRequest(t, router, "GET", "/sub/"+subToken, "", nil)
	if subResp.Code != 200 {
		t.Fatalf("get subscription: %d body=%s", subResp.Code, subResp.Raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(subResp.Raw))
	if err != nil {
		t.Fatalf("subscription body is not valid base64: %v", err)
	}
	links := strings.Split(strings.TrimSpace(string(decoded)), "\n")
	if len(links) != 2 {
		t.Fatalf("expected exactly 2 links, got %d: %q", len(links), links)
	}
	if !strings.Contains(links[0], "2.2.2.2") {
		t.Errorf("first link = %q, want HostB (priority 1) first despite tag A sorting alphabetically first", links[0])
	}
	if !strings.Contains(links[1], "1.1.1.1") {
		t.Errorf("second link = %q, want HostA (priority 2) second", links[1])
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

// TestSubscriptionUnknownFormatRejected is a regression test for a real gap
// found via this project's own stress-testing audit: GET /sub/:token/:format
// used to accept any string and silently fall back to v2ray links with a
// 200, instead of rejecting an unsupported format the way app/routers/
// subscription.py's own Path regex whitelist does.
func TestSubscriptionUnknownFormatRejected(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "unknown_format_test_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	subToken := subscription.CreateToken("unknown_format_test_user", []byte(testSubSecret))

	resp := doRequest(t, router, "GET", "/sub/"+subToken+"/not-a-real-format", "", nil)
	if resp.Code != 404 {
		t.Fatalf("expected 404 for an unsupported format, got %d body=%s", resp.Code, resp.Raw)
	}
}

func TestSubscriptionClashFormat(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "VMess WS", "protocol": "vmess", "network": "ws", "security": "tls"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VMess WS": []map[string]interface{}{{"remark": "ClashNode", "address": "1.2.3.4", "port": 443, "sni": "example.com", "path": "/ws", "security": "tls"}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "clash_sub_test", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})

	subToken := subscription.CreateToken("clash_sub_test", []byte(testSubSecret))
	resp := doRequest(t, router, "GET", "/sub/"+subToken+"/clash", "", nil)
	if resp.Code != 200 {
		t.Fatalf("get clash subscription: %d body=%s", resp.Code, resp.Raw)
	}
	body := string(resp.Raw)
	if !strings.Contains(body, "type: vmess") {
		t.Errorf("clash config missing vmess proxy: %s", body)
	}
	if !strings.Contains(body, "name: ClashNode") {
		t.Errorf("clash config missing the host's remark: %s", body)
	}
	if !strings.Contains(body, "proxy-groups:") || !strings.Contains(body, "rules:") {
		t.Errorf("clash config missing required top-level keys: %s", body)
	}
}

func TestSubscriptionClashMetaFormatIncludesReality(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "VLESS Reality", "protocol": "vless", "network": "tcp", "security": "reality",
			"reality_private_key": "SGVsbG9Xb3JsZEhlbGxvV29ybGRIZWxsb1dvcmxkMTI",
			"reality_short_ids":   []string{"abcd1234"}},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS Reality": []map[string]interface{}{{"remark": "MetaNode", "address": "1.2.3.4", "port": 443, "sni": "example.com", "security": "reality"}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "clash_meta_sub_test", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	subToken := subscription.CreateToken("clash_meta_sub_test", []byte(testSubSecret))
	resp := doRequest(t, router, "GET", "/sub/"+subToken+"/clash-meta", "", nil)
	if resp.Code != 200 {
		t.Fatalf("get clash-meta subscription: %d body=%s", resp.Code, resp.Raw)
	}
	body := string(resp.Raw)
	if !strings.Contains(body, "type: vless") {
		t.Errorf("clash-meta config missing vless proxy (plain clash drops it, meta must not): %s", body)
	}
	if !strings.Contains(body, "reality-opts:") {
		t.Errorf("clash-meta config missing reality-opts: %s", body)
	}
}

// TestSubscriptionOutlineFormatIncludesEveryServer is the real regression
// test for the known Python bug this Go port deliberately does not carry
// forward: OutlineConfiguration.add_directly there does a flat dict.update()
// per host, so a user with more than one shadowsocks host silently loses
// every server but the last. Two real hosts here, both must survive.
func TestSubscriptionOutlineFormatIncludesEveryServer(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "SS One", "protocol": "shadowsocks"}, {"tag": "SS Two", "protocol": "shadowsocks"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"SS One": []map[string]interface{}{{"remark": "OutlineOne", "address": "1.2.3.4", "port": 8388, "security": "none"}},
		"SS Two": []map[string]interface{}{{"remark": "OutlineTwo", "address": "5.6.7.8", "port": 8389, "security": "none"}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "outline_sub_test", "proxies": map[string]interface{}{"shadowsocks": map[string]interface{}{}},
	})

	subToken := subscription.CreateToken("outline_sub_test", []byte(testSubSecret))
	resp := doRequest(t, router, "GET", "/sub/"+subToken+"/outline", "", nil)
	if resp.Code != 200 {
		t.Fatalf("get outline subscription: %d body=%s", resp.Code, resp.Raw)
	}
	var doc struct {
		Version int `json:"version"`
		Servers []struct {
			ID      string `json:"id"`
			Remarks string `json:"remarks"`
			Server  string `json:"server"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(resp.Raw, &doc); err != nil {
		t.Fatalf("outline response is not valid SIP008 JSON: %v\n%s", err, resp.Raw)
	}
	if doc.Version != 1 {
		t.Errorf("version = %d, want 1", doc.Version)
	}
	if len(doc.Servers) != 2 {
		t.Fatalf("servers = %d, want 2 (both hosts must survive, not just the last one)", len(doc.Servers))
	}
	remarks := map[string]bool{doc.Servers[0].Remarks: true, doc.Servers[1].Remarks: true}
	if !remarks["OutlineOne"] || !remarks["OutlineTwo"] {
		t.Errorf("expected both OutlineOne and OutlineTwo, got %+v", doc.Servers)
	}
}

func TestSubscriptionV2rayJSONFormat(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "VLESS gRPC", "protocol": "vless", "network": "grpc", "security": "tls"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS gRPC": []map[string]interface{}{{"remark": "JsonNode", "address": "1.2.3.4", "port": 443, "sni": "example.com", "path": "grpc-service", "security": "tls"}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "v2ray_json_sub_test", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	subToken := subscription.CreateToken("v2ray_json_sub_test", []byte(testSubSecret))
	resp := doRequest(t, router, "GET", "/sub/"+subToken+"/v2ray-json", "", nil)
	if resp.Code != 200 {
		t.Fatalf("get v2ray-json subscription: %d body=%s", resp.Code, resp.Raw)
	}
	var configs []map[string]interface{}
	if err := json.Unmarshal(resp.Raw, &configs); err != nil {
		t.Fatalf("v2ray-json response is not a JSON array: %v\n%s", err, resp.Raw)
	}
	if len(configs) != 1 {
		t.Fatalf("expected exactly one config in the array, got %d", len(configs))
	}
	if configs[0]["remarks"] != "JsonNode" {
		t.Errorf("remarks = %v, want JsonNode", configs[0]["remarks"])
	}
	outbounds, _ := configs[0]["outbounds"].([]interface{})
	if len(outbounds) != 3 {
		t.Fatalf("expected 3 outbounds (proxy + direct + block), got %d: %v", len(outbounds), outbounds)
	}
}

// TestSubscriptionAutoDetectsFormatFromUserAgent proves the real User-Agent
// sniffing table (not just the explicit /:format route) actually routes to
// each new format - a client that never asks for a format explicitly still
// gets the right one.
func TestSubscriptionAutoDetectsFormatFromUserAgent(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "ua_detect_test_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	subToken := subscription.CreateToken("ua_detect_test_user", []byte(testSubSecret))

	cases := []struct {
		userAgent    string
		wantContains string
	}{
		{"ClashMetaForAndroid/2.10", "proxies:"},
		{"Clash/1.0", "proxies:"},
		{"SFA/1.0", `"outbounds"`},
		{"Outline/1.3", `"servers"`},
		{"v2rayNG/1.9.0", `"outbounds"`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/sub/"+subToken, nil)
		req.Header.Set("User-Agent", tc.userAgent)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("User-Agent %q: %d body=%s", tc.userAgent, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.wantContains) {
			t.Errorf("User-Agent %q: expected body to contain %q, got: %s", tc.userAgent, tc.wantContains, rec.Body.String())
		}
	}
}
