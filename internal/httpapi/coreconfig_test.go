package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestGetCoreConfigDefaults(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "GET", "/api/settings/core-config", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get core config: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["log_level"] != "warn" {
		t.Errorf("log_level = %v, want warn", resp.Body["log_level"])
	}
	if resp.Body["sniff_enabled"] != true {
		t.Errorf("sniff_enabled = %v, want true", resp.Body["sniff_enabled"])
	}
	if outbounds, ok := resp.Body["outbounds"].([]interface{}); !ok || len(outbounds) != 0 {
		t.Errorf("outbounds = %v, want an empty array", resp.Body["outbounds"])
	}
}

func TestUpdateCoreConfigFullRoundTrip(t *testing.T) {
	router, token := newTestRouter(t)
	payload := map[string]interface{}{
		"log_level":     "debug",
		"sniff_enabled": false,
		"outbounds": []map[string]interface{}{
			{"tag": "upstream", "type": "socks", "server": "10.0.0.1", "server_port": 1080},
		},
		"routing_rules": []map[string]interface{}{
			{"ip_is_private": true, "outbound_tag": "block"},
			{"domain_suffix": []string{"example.com"}, "outbound_tag": "upstream"},
		},
		"dns_servers": []map[string]interface{}{
			{"tag": "cf", "type": "udp", "address": "1.1.1.1"},
		},
	}
	putResp := doRequest(t, router, "PUT", "/api/settings/core-config", token, payload)
	if putResp.Code != http.StatusOK {
		t.Fatalf("put core config: %d %v", putResp.Code, putResp.Body)
	}
	if putResp.Body["log_level"] != "debug" {
		t.Errorf("log_level = %v, want debug", putResp.Body["log_level"])
	}

	getResp := doRequest(t, router, "GET", "/api/settings/core-config", token, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get core config: %d %v", getResp.Code, getResp.Body)
	}
	outbounds := getResp.Body["outbounds"].([]interface{})
	if len(outbounds) != 1 {
		t.Fatalf("outbounds = %v, want 1 entry", outbounds)
	}
	ob := outbounds[0].(map[string]interface{})
	if ob["tag"] != "upstream" || ob["type"] != "socks" {
		t.Errorf("outbound = %v, want tag=upstream type=socks", ob)
	}
	rules := getResp.Body["routing_rules"].([]interface{})
	if len(rules) != 2 {
		t.Fatalf("routing_rules = %v, want 2 entries", rules)
	}
	dnsServers := getResp.Body["dns_servers"].([]interface{})
	if len(dnsServers) != 1 {
		t.Fatalf("dns_servers = %v, want 1 entry", dnsServers)
	}
}

func TestUpdateCoreConfigRejectsInvalidLogLevel(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{"log_level": "not-a-real-level"})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("invalid log_level: %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigRejectsReservedOutboundTag(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{{"tag": "direct", "type": "direct"}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("outbound tag 'direct' (reserved): %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigRejectsDuplicateOutboundTag(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{
			{"tag": "up1", "type": "direct"},
			{"tag": "up1", "type": "block"},
		},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("duplicate outbound tag: %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigRejectsRuleTargetingUnknownOutbound(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"routing_rules": []map[string]interface{}{{"ip_is_private": true, "outbound_tag": "no-such-outbound"}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("rule targeting unknown outbound: %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigRejectsRuleTargetingUnknownInbound(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"routing_rules": []map[string]interface{}{{"inbound": []string{"no-such-inbound"}, "outbound_tag": "block"}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("rule targeting unknown inbound: %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigAcceptsRuleTargetingARealInbound(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})

	putResp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"routing_rules": []map[string]interface{}{{"inbound": []string{"VLESS TCP"}, "outbound_tag": "block"}},
	})
	if putResp.Code != http.StatusOK {
		t.Fatalf("put core config with a real inbound reference: %d %v", putResp.Code, putResp.Body)
	}

	getResp := doRequest(t, router, "GET", "/api/settings/core-config", token, nil)
	rules := getResp.Body["routing_rules"].([]interface{})
	if len(rules) != 1 {
		t.Fatalf("routing_rules = %v, want 1 entry", rules)
	}
	rule := rules[0].(map[string]interface{})
	inbound := rule["inbound"].([]interface{})
	if len(inbound) != 1 || inbound[0] != "VLESS TCP" {
		t.Errorf("rule.inbound = %v, want [\"VLESS TCP\"]", inbound)
	}
}

func TestUpdateCoreConfigRoundTripsBindInterface(t *testing.T) {
	router, token := newTestRouter(t)
	putResp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{
			{"tag": "germany", "type": "direct", "bind_interface": "wg-germany"},
		},
	})
	if putResp.Code != http.StatusOK {
		t.Fatalf("put core config with bind_interface: %d %v", putResp.Code, putResp.Body)
	}
	outbounds := putResp.Body["outbounds"].([]interface{})
	ob := outbounds[0].(map[string]interface{})
	if ob["bind_interface"] != "wg-germany" {
		t.Errorf("bind_interface = %v, want wg-germany", ob["bind_interface"])
	}
}

func TestUpdateCoreConfigRejectsSocksOutboundMissingServer(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{{"tag": "up1", "type": "socks"}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("socks outbound missing server: %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigAcceptsEveryRealProtocolOutbound(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{
			{"tag": "ss1", "type": "shadowsocks", "server": "203.0.113.1", "server_port": 8388, "method": "aes-256-gcm", "password": "pw"},
			{"tag": "vm1", "type": "vmess", "server": "203.0.113.1", "server_port": 443, "uuid": "8f8a4c1e-1e2a-4b8a-9b1a-0000000000aa", "security": "auto"},
			{"tag": "tr1", "type": "trojan", "server": "example.com", "server_port": 443, "password": "pw", "tls_enabled": true, "tls_server_name": "example.com"},
			{"tag": "vl1", "type": "vless", "server": "203.0.113.1", "server_port": 443, "uuid": "8f8a4c1e-1e2a-4b8a-9b1a-0000000000ab"},
			{"tag": "h2", "type": "hysteria2", "server": "example.com", "server_port": 443, "password": "pw"},
			{"tag": "tu1", "type": "tuic", "server": "example.com", "server_port": 443, "uuid": "8f8a4c1e-1e2a-4b8a-9b1a-0000000000ac", "password": "pw", "congestion_control": "bbr"},
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("put core config with every protocol: %d %v", resp.Code, resp.Body)
	}
	outbounds := resp.Body["outbounds"].([]interface{})
	if len(outbounds) != 6 {
		t.Fatalf("outbounds = %v, want 6", outbounds)
	}
}

func TestUpdateCoreConfigRejectsShadowsocksInvalidMethod(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{
			{"tag": "ss1", "type": "shadowsocks", "server": "203.0.113.1", "server_port": 8388, "method": "not-a-real-method", "password": "pw"},
		},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("shadowsocks invalid method: %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigRejectsVmessMissingUUID(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{
			{"tag": "vm1", "type": "vmess", "server": "203.0.113.1", "server_port": 443, "security": "auto"},
		},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("vmess missing uuid: %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigRejectsTuicInvalidCongestionControl(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{
			{"tag": "tu1", "type": "tuic", "server": "example.com", "server_port": 443, "uuid": "8f8a4c1e-1e2a-4b8a-9b1a-0000000000ac", "password": "pw", "congestion_control": "not-a-real-algo"},
		},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("tuic invalid congestion_control: %d, want 422: %v", resp.Code, resp.Body)
	}
}

func TestUpdateCoreConfigRejectsUnknownDNSType(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"dns_servers": []map[string]interface{}{{"tag": "d1", "type": "not-a-real-type"}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown dns type: %d, want 422: %v", resp.Code, resp.Body)
	}
}

// TestValidateCoreConfigInboundPortAndDirectFallback covers every rule the
// two multi-port/fail-open fields add. It calls the validator directly (it is
// a pure function) so each rule is pinned by its own message; the PUT
// handler's 422 mapping is exercised once below.
func TestValidateCoreConfigInboundPortAndDirectFallback(t *testing.T) {
	wgOutbound := outboundDTO{Tag: "uk", Type: "direct", BindInterface: "wg-uk", DirectFallback: true}
	cases := []struct {
		name string
		dto  coreConfigDTO
		want string // "" means valid, otherwise a substring of the message
	}{
		{"valid rule with ports", coreConfigDTO{RoutingRules: []routingRuleDTO{{Inbound: []string{"main"}, InboundPort: []int{1, 20004, 65535}, OutboundTag: "block"}}}, ""},
		{"inbound_port without inbound", coreConfigDTO{RoutingRules: []routingRuleDTO{{InboundPort: []int{20004}, OutboundTag: "block"}}}, "inbound_port requires a non-empty inbound"},
		{"port zero", coreConfigDTO{RoutingRules: []routingRuleDTO{{Inbound: []string{"main"}, InboundPort: []int{0}, OutboundTag: "block"}}}, "invalid inbound_port 0"},
		{"port negative", coreConfigDTO{RoutingRules: []routingRuleDTO{{Inbound: []string{"main"}, InboundPort: []int{-5}, OutboundTag: "block"}}}, "invalid inbound_port -5"},
		{"port too large", coreConfigDTO{RoutingRules: []routingRuleDTO{{Inbound: []string{"main"}, InboundPort: []int{65536}, OutboundTag: "block"}}}, "invalid inbound_port 65536"},
		{"duplicate port", coreConfigDTO{RoutingRules: []routingRuleDTO{{Inbound: []string{"main"}, InboundPort: []int{20004, 20005, 20004}, OutboundTag: "block"}}}, "duplicate inbound_port 20004"},

		{"valid fallback", coreConfigDTO{Outbounds: []outboundDTO{wgOutbound}}, ""},
		{"fallback without bind_interface", coreConfigDTO{Outbounds: []outboundDTO{{Tag: "uk", Type: "direct", DirectFallback: true}}}, "outbound uk: direct_fallback requires bind_interface"},
		{"fallback on a leaf proxy type is fine", coreConfigDTO{Outbounds: []outboundDTO{{Tag: "s", Type: "socks", Server: "1.2.3.4", ServerPort: 1080, BindInterface: "wg0", DirectFallback: true}}}, ""},
		{"fallback on selector", coreConfigDTO{Outbounds: []outboundDTO{wgOutbound, {Tag: "pick", Type: "selector", Outbounds: []string{"uk"}, BindInterface: "wg0", DirectFallback: true}}}, "outbound pick: direct_fallback is not allowed for type selector"},
		{"fallback on urltest", coreConfigDTO{Outbounds: []outboundDTO{wgOutbound, {Tag: "auto", Type: "urltest", Outbounds: []string{"uk"}, BindInterface: "wg0", DirectFallback: true}}}, "outbound auto: direct_fallback is not allowed for type urltest"},
		{"fallback on block", coreConfigDTO{Outbounds: []outboundDTO{{Tag: "b", Type: "block", BindInterface: "wg0", DirectFallback: true}}}, "outbound b: direct_fallback is not allowed for type block"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateCoreConfig(tc.dto)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("validateCoreConfig = %q, want valid", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("validateCoreConfig = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestUpdateCoreConfigRejectsBadInboundPortAndDirectFallback(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "main", "protocol": "vless"}})

	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"routing_rules": []map[string]interface{}{{"inbound": []string{"main"}, "inbound_port": []int{70000}, "outbound_tag": "block"}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("out-of-range inbound_port: %d, want 422: %v", resp.Code, resp.Body)
	}
	resp = doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{{"tag": "uk", "type": "direct", "direct_fallback": true}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("direct_fallback without bind_interface: %d, want 422: %v", resp.Code, resp.Body)
	}
	if detail, _ := resp.Body["detail"].(string); !strings.Contains(detail, "direct_fallback requires bind_interface") {
		t.Errorf("detail = %q, want the direct_fallback message", detail)
	}
}

// TestCoreConfigRoundTripsInboundPortAndDirectFallback proves the two new
// fields survive every hop: the PUT response, a later GET, and - the one that
// matters to a node - the core section of the node-config payload.
func TestCoreConfigRoundTripsInboundPortAndDirectFallback(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "core-roundtrip-node")
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "main", "protocol": "vless"}})

	putResp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{
			{"tag": "uk", "type": "direct", "bind_interface": "wg-uk", "direct_fallback": true},
			{"tag": "de", "type": "direct", "bind_interface": "wg-de"},
		},
		"routing_rules": []map[string]interface{}{
			{"inbound": []string{"main"}, "inbound_port": []int{20004, 20005}, "outbound_tag": "uk"},
			{"inbound": []string{"main"}, "outbound_tag": "de"},
		},
	})
	if putResp.Code != http.StatusOK {
		t.Fatalf("put core config: %d %v", putResp.Code, putResp.Body)
	}
	assertCoreRoundTrip(t, "PUT response", putResp.Body)

	getResp := doRequest(t, router, "GET", "/api/settings/core-config", token, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get core config: %d %v", getResp.Code, getResp.Body)
	}
	assertCoreRoundTrip(t, "GET response", getResp.Body)

	nodeResp := doRequest(t, router, "GET", "/api/internal/node-config", secret, nil)
	if nodeResp.Code != http.StatusOK {
		t.Fatalf("get node config: %d %v", nodeResp.Code, nodeResp.Body)
	}
	assertCoreRoundTrip(t, "node-config core", nodeResp.Body["core"].(map[string]interface{}))
}

func assertCoreRoundTrip(t *testing.T, where string, core map[string]interface{}) {
	t.Helper()
	outbounds := core["outbounds"].([]interface{})
	uk, de := outbounds[0].(map[string]interface{}), outbounds[1].(map[string]interface{})
	if uk["direct_fallback"] != true {
		t.Errorf("%s: uk.direct_fallback = %v, want true", where, uk["direct_fallback"])
	}
	if _, has := de["direct_fallback"]; has {
		t.Errorf("%s: de.direct_fallback = %v, want the key omitted when false", where, de["direct_fallback"])
	}
	rules := core["routing_rules"].([]interface{})
	ported, plain := rules[0].(map[string]interface{}), rules[1].(map[string]interface{})
	ports, _ := ported["inbound_port"].([]interface{})
	if len(ports) != 2 || ports[0] != float64(20004) || ports[1] != float64(20005) {
		t.Errorf("%s: rule 0 inbound_port = %v, want [20004 20005]", where, ported["inbound_port"])
	}
	if _, has := plain["inbound_port"]; has {
		t.Errorf("%s: rule 1 inbound_port = %v, want the key omitted when unset", where, plain["inbound_port"])
	}
}
