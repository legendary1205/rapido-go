package httpapi

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestListInboundsDetailedReturnsRealFields(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{
			"tag": "VLESS Reality", "protocol": "vless", "network": "tcp", "security": "reality",
			"reality_private_key": "priv-key-value", "reality_short_ids": []string{"ab12"},
			"reality_server_name": "example.com", "reality_server_port": 443,
		},
	})

	resp := doRequest(t, router, "GET", "/api/inbounds/detail", token, nil)
	if resp.Code != 200 {
		t.Fatalf("list detailed inbounds: %d %v", resp.Code, resp.Body)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(resp.Raw, &rows); err != nil {
		t.Fatalf("decode inbounds detail: %v", err)
	}
	var found map[string]interface{}
	for _, r := range rows {
		if r["tag"] == "VLESS Reality" {
			found = r
		}
	}
	if found == nil {
		t.Fatalf("VLESS Reality not found in %v", rows)
	}
	if found["protocol"] != "vless" || found["network"] != "tcp" || found["security"] != "reality" {
		t.Errorf("unexpected shape: %v", found)
	}
	if found["reality_private_key"] != "priv-key-value" {
		t.Errorf("reality_private_key = %v, want priv-key-value", found["reality_private_key"])
	}
	if found["reality_server_port"].(float64) != 443 {
		t.Errorf("reality_server_port = %v, want 443", found["reality_server_port"])
	}
}

func TestInboundsSyncAndDetailRoundTripATLSCertificate(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{
			"tag": "TLS Inbound", "protocol": "vless", "network": "tcp", "security": "tls",
			"tls_certificate": testCertPEM, "tls_key": testKeyPEM, "tls_server_name": "example.test",
		},
	})

	resp := doRequest(t, router, "GET", "/api/inbounds/detail", token, nil)
	if resp.Code != 200 {
		t.Fatalf("list detailed inbounds: %d %v", resp.Code, resp.Body)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(resp.Raw, &rows); err != nil {
		t.Fatalf("decode inbounds detail: %v", err)
	}
	var found map[string]interface{}
	for _, r := range rows {
		if r["tag"] == "TLS Inbound" {
			found = r
		}
	}
	if found == nil {
		t.Fatalf("TLS Inbound not found in %v", rows)
	}
	if found["security"] != "tls" {
		t.Errorf("security = %v, want tls", found["security"])
	}
	if found["tls_certificate"] != testCertPEM {
		t.Errorf("tls_certificate did not round-trip byte-for-byte")
	}
	if found["tls_key"] != testKeyPEM {
		t.Errorf("tls_key did not round-trip byte-for-byte")
	}
	if found["tls_server_name"] != "example.test" {
		t.Errorf("tls_server_name = %v, want example.test", found["tls_server_name"])
	}
}

func TestDeleteInboundRemovesItAndCascadesItsHosts(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "To Delete", "protocol": "vless"}})

	hostsResp := doRequest(t, router, "GET", "/api/hosts", token, nil)
	if hostsResp.Code != 200 {
		t.Fatalf("get hosts before delete: %d %v", hostsResp.Code, hostsResp.Body)
	}
	if _, ok := hostsResp.Body["To Delete"]; !ok {
		t.Fatalf("expected a default host under 'To Delete' before deletion, got %v", hostsResp.Body)
	}

	// The tag has a real space in it (a legitimate remark, matching this
	// project's own convention - "VMess TCP" etc.) - it must be
	// URL-encoded to be a valid request path, the same way the frontend's
	// delete call needs to encodeURIComponent it.
	del := doRequest(t, router, "DELETE", "/api/inbounds/"+url.PathEscape("To Delete"), token, nil)
	if del.Code != 200 {
		t.Fatalf("delete inbound: %d %v", del.Code, del.Body)
	}

	hostsAfter := doRequest(t, router, "GET", "/api/hosts", token, nil)
	if hostsAfter.Code != 200 {
		t.Fatalf("get hosts after delete: %d %v", hostsAfter.Code, hostsAfter.Body)
	}
	if _, ok := hostsAfter.Body["To Delete"]; ok {
		t.Errorf("expected 'To Delete''s hosts to be cascade-deleted along with the inbound, still present: %v", hostsAfter.Body)
	}

	getInboundsResp := doRequest(t, router, "GET", "/api/inbounds", token, nil)
	if tags, ok := getInboundsResp.Body["vless"].([]interface{}); ok {
		for _, tag := range tags {
			if tag == "To Delete" {
				t.Errorf("deleted inbound tag still present in GET /api/inbounds: %v", tags)
			}
		}
	}
}

func TestDeleteInboundNotFound(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "DELETE", "/api/inbounds/does-not-exist", token, nil)
	if resp.Code != 404 {
		t.Errorf("delete nonexistent inbound: got %d, want 404 (%v)", resp.Code, resp.Body)
	}
}

func TestInboundsDetailAndDeleteAreSudoOnly(t *testing.T) {
	router, sudoToken := newTestRouter(t)
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "inb-owner", "password": "pw12345", "is_sudo": false})
	nonSudo := loginAs(t, router, "inb-owner", "pw12345")

	if resp := doRequest(t, router, "GET", "/api/inbounds/detail", nonSudo, nil); resp.Code != 403 {
		t.Errorf("GET /inbounds/detail as non-sudo: got %d, want 403 (%v)", resp.Code, resp.Body)
	}
	if resp := doRequest(t, router, "DELETE", "/api/inbounds/anything", nonSudo, nil); resp.Code != 403 {
		t.Errorf("DELETE /inbounds/:tag as non-sudo: got %d, want 403 (%v)", resp.Code, resp.Body)
	}
}
