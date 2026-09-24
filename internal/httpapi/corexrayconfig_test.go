package httpapi

import (
	"net/http"
	"testing"
)

func TestGetCoreVersionReflectsWhetherAnyInboundIsAutoSyncEligible(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "GET", "/api/core", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get core version: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["started"] != false {
		t.Errorf("started = %v, want false with no inbounds yet", resp.Body["started"])
	}
	if resp.Body["version"] == "" || resp.Body["version"] == nil {
		t.Errorf("version = %v, want a non-empty string", resp.Body["version"])
	}

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "n1", "address": "1.2.3.4", "port": 8443}},
	})

	resp = doRequest(t, router, "GET", "/api/core", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get core version (2nd): %d %v", resp.Code, resp.Body)
	}
	if resp.Body["started"] != true {
		t.Errorf("started = %v, want true once a real auto-sync inbound exists", resp.Body["started"])
	}
}

func TestGetRawXrayConfigReturnsRealXrayShapedJSON(t *testing.T) {
	router, token := newTestRouter(t)

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{
			"tag": "TLS Inbound Real", "protocol": "vless", "security": "tls",
			"tls_certificate": testCertPEM, "tls_key": testKeyPEM, "tls_server_name": "example.test",
		},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"TLS Inbound Real": []map[string]interface{}{{"remark": "n1", "address": "1.2.3.4", "port": 8444}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "core_cfg_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{"flow": "xtls-rprx-vision"}},
	})

	resp := doRequest(t, router, "GET", "/api/core/config", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get raw xray config: %d %v", resp.Code, resp.Body)
	}

	// This is real, raw Xray JSON (log/inbounds/outbounds/routing/dns at
	// the top level) - NOT this codebase's own sing-box-flavored Core
	// Config DTO (which has log_level/sniff_enabled/routing_rules/
	// dns_servers instead), since an external reseller bot
	// parses exactly the former.
	if _, ok := resp.Body["log_level"]; ok {
		t.Fatalf("response looks like the sing-box Core Config DTO, not real Xray JSON: %v", resp.Body)
	}
	inbounds, ok := resp.Body["inbounds"].([]interface{})
	if !ok || len(inbounds) != 1 {
		t.Fatalf("inbounds = %v, want exactly 1", resp.Body["inbounds"])
	}
	in := inbounds[0].(map[string]interface{})
	if in["tag"] != "TLS Inbound Real" || in["protocol"] != "vless" || in["port"].(float64) != 8444 {
		t.Errorf("inbound = %v, want tag=TLS Inbound Real protocol=vless port=8444", in)
	}
	stream := in["streamSettings"].(map[string]interface{})
	if stream["security"] != "tls" {
		t.Errorf("streamSettings.security = %v, want tls", stream["security"])
	}
	tlsSettings := stream["tlsSettings"].(map[string]interface{})
	if tlsSettings["serverName"] != "example.test" {
		t.Errorf("tlsSettings.serverName = %v, want example.test", tlsSettings["serverName"])
	}
	clients := in["settings"].(map[string]interface{})["clients"].([]interface{})
	if len(clients) != 1 {
		t.Fatalf("clients = %v, want 1", clients)
	}
	client := clients[0].(map[string]interface{})
	if client["email"] != "core_cfg_user" || client["flow"] != "xtls-rprx-vision" {
		t.Errorf("client = %v, want email=core_cfg_user flow=xtls-rprx-vision", client)
	}
	if _, hasOutbounds := resp.Body["outbounds"]; !hasOutbounds {
		t.Errorf("expected an outbounds array even with no custom outbounds configured, got %v", resp.Body)
	}
	if _, hasRouting := resp.Body["routing"]; !hasRouting {
		t.Errorf("expected a routing object, got %v", resp.Body)
	}
}

// TestGetRawXrayConfigExportsMultiPortInboundAndLocalPortRules covers the
// wiring the xrayimport unit test cannot: buildRawXrayInbounds feeding each
// inbound's host ports, and coreRoutingRulesToExport carrying inbound_port.
func TestGetRawXrayConfigExportsMultiPortInboundAndLocalPortRules(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "main", "protocol": "vless"},
		{"tag": "solo", "protocol": "trojan"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"main": multiPortHosts(),
		"solo": []map[string]interface{}{{"remark": "s", "address": "1.2.3.4", "port": 2087}},
	})
	putResp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{{"tag": "uk", "type": "direct", "bind_interface": "wg-uk", "direct_fallback": true}},
		"routing_rules": []map[string]interface{}{
			{"inbound": []string{"main"}, "inbound_port": []int{20001, 20002}, "outbound_tag": "uk"},
		},
	})
	if putResp.Code != http.StatusOK {
		t.Fatalf("put core config: %d %v", putResp.Code, putResp.Body)
	}

	resp := doRequest(t, router, "GET", "/api/core/config", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get raw xray config: %d %v", resp.Code, resp.Body)
	}
	byTag := map[string]map[string]interface{}{}
	for _, i := range resp.Body["inbounds"].([]interface{}) {
		in := i.(map[string]interface{})
		byTag[in["tag"].(string)] = in
	}
	if got := byTag["main"]["port"]; got != "20000,20001,20002" {
		t.Errorf("main port = %#v, want the comma string \"20000,20001,20002\"", got)
	}
	if got := byTag["solo"]["port"]; got != float64(2087) {
		t.Errorf("solo port = %#v, want the bare number 2087", got)
	}
	rules := resp.Body["routing"].(map[string]interface{})["rules"].([]interface{})
	if len(rules) != 1 {
		t.Fatalf("rules = %v, want 1", rules)
	}
	rule := rules[0].(map[string]interface{})
	if rule["localPort"] != "20001,20002" {
		t.Errorf("rule localPort = %#v, want \"20001,20002\"", rule["localPort"])
	}
	if tags, _ := rule["inboundTag"].([]interface{}); len(tags) != 1 || tags[0] != "main" {
		t.Errorf("rule inboundTag = %v, want [main]", rule["inboundTag"])
	}
}
