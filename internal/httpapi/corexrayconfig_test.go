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
	// dns_servers instead), since a genuine-Marzban-API reseller bot
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
