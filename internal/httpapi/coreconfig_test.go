package httpapi

import (
	"net/http"
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
