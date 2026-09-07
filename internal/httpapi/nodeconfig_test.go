package httpapi

import (
	"net/http"
	"testing"
)

func TestGetNodeConfigBuildsInboundFromActiveUsersOnly(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "config-test-node-1")

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "n1", "address": "1.2.3.4", "port": 8443}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "nc_active_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "nc_onhold_user", "status": "on_hold", "on_hold_expire_duration": 86400,
		"proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	createResp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "nc_disabled_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create user to disable: %d %v", createResp.Code, createResp.Body)
	}
	disableResp := doRequest(t, router, "PUT", "/api/user/nc_disabled_user", token, map[string]interface{}{"status": "disabled"})
	if disableResp.Code != http.StatusOK {
		t.Fatalf("disable user: %d %v", disableResp.Code, disableResp.Body)
	}

	resp := doRequest(t, router, "GET", "/api/internal/node-config", secret, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get node config: %d %v", resp.Code, resp.Body)
	}
	inbounds := resp.Body["inbounds"].([]interface{})
	if len(inbounds) != 1 {
		t.Fatalf("inbounds = %v, want 1", inbounds)
	}
	in := inbounds[0].(map[string]interface{})
	if in["tag"] != "VLESS TCP" || in["protocol"] != "vless" {
		t.Errorf("inbound = %v, want tag=VLESS TCP protocol=vless", in)
	}
	if port, ok := in["listen_port"].(float64); !ok || int(port) != 8443 {
		t.Errorf("listen_port = %v, want 8443 (from the host's port)", in["listen_port"])
	}
	users := in["users"].([]interface{})
	names := make(map[string]bool, len(users))
	for _, u := range users {
		names[u.(map[string]interface{})["name"].(string)] = true
	}
	if len(users) != 2 || !names["nc_active_user"] || !names["nc_onhold_user"] {
		t.Errorf("users = %v, want exactly [nc_active_user, nc_onhold_user] (disabled user excluded)", users)
	}
}

func TestGetNodeConfigExcludesTLSInboundsWithoutACertificate(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "config-test-node-2")

	// A security=tls inbound with no certificate/key yet stays excluded
	// from auto-sync (see ListAutoSyncInbounds's own doc comment) - there
	// would be nothing real to serve. See
	// TestGetNodeConfigIncludesTLSInboundsWithARealCertificate below for
	// the companion case once a certificate is actually set.
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "TLS Inbound", "protocol": "vless", "security": "tls"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"TLS Inbound": []map[string]interface{}{{"remark": "n1", "address": "1.2.3.4", "port": 8443}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "nc_tls_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	resp := doRequest(t, router, "GET", "/api/internal/node-config", secret, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get node config: %d %v", resp.Code, resp.Body)
	}
	inbounds := resp.Body["inbounds"].([]interface{})
	for _, i := range inbounds {
		if i.(map[string]interface{})["tag"] == "TLS Inbound" {
			t.Fatalf("a security=tls inbound with no certificate yet must stay excluded from auto-sync, got it in the response: %v", inbounds)
		}
	}
}

func TestGetNodeConfigIncludesTLSInboundsWithARealCertificate(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "config-test-node-tls")

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{
			"tag": "TLS Inbound Real", "protocol": "vless", "security": "tls",
			"tls_certificate": testCertPEM, "tls_key": testKeyPEM, "tls_server_name": "example.test",
		},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"TLS Inbound Real": []map[string]interface{}{{"remark": "n1", "address": "1.2.3.4", "port": 8444}},
	})

	resp := doRequest(t, router, "GET", "/api/internal/node-config", secret, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get node config: %d %v", resp.Code, resp.Body)
	}
	inbounds := resp.Body["inbounds"].([]interface{})
	var found map[string]interface{}
	for _, i := range inbounds {
		if i.(map[string]interface{})["tag"] == "TLS Inbound Real" {
			found = i.(map[string]interface{})
		}
	}
	if found == nil {
		t.Fatalf("a security=tls inbound with a real certificate must be included in auto-sync, got %v", inbounds)
	}
	tls, ok := found["tls"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected a populated tls object, got %v", found)
	}
	if tls["certificate"] != testCertPEM || tls["key"] != testKeyPEM || tls["server_name"] != "example.test" {
		t.Errorf("tls spec = %v, want the real certificate/key/server_name round-tripped", tls)
	}
	if _, hasReality := tls["reality"]; hasReality {
		t.Errorf("a plain tls inbound must not carry a reality section: %v", tls)
	}
}

func TestGetNodeConfigIncludesCoreConfigAndRealHostChangeBustsCache(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "config-test-node-3")

	doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"log_level": "debug", "sniff_enabled": true,
	})

	first := doRequest(t, router, "GET", "/api/internal/node-config", secret, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("get node config: %d %v", first.Code, first.Body)
	}
	core := first.Body["core"].(map[string]interface{})
	if core["log_level"] != "debug" {
		t.Errorf("core.log_level = %v, want debug", core["log_level"])
	}
	firstVersion := first.Body["version"]

	// A host write explicitly busts the node-config cache (see
	// handlePutHosts's InvalidateNodeConfigPayload call) - the very next
	// fetch must reflect it immediately, not after the 2s cache TTL.
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VLESS TCP": []map[string]interface{}{{"remark": "n1", "address": "1.2.3.4", "port": 9999}},
	})

	second := doRequest(t, router, "GET", "/api/internal/node-config", secret, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("get node config (2nd): %d %v", second.Code, second.Body)
	}
	if second.Body["version"] == firstVersion {
		t.Error("version did not change after adding a host+inbound - cache invalidation on write isn't working")
	}
	inbounds := second.Body["inbounds"].([]interface{})
	if len(inbounds) != 1 {
		t.Fatalf("inbounds = %v, want 1 after adding VLESS TCP", inbounds)
	}
}
