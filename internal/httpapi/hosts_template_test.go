package httpapi

import (
	"fmt"
	"testing"
)

func TestSyncInboundCreatesDefaultHost(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VLESS TCP REALITY", "protocol": "vless"}})
	if resp.Code != 200 || resp.Body["created"].(float64) != 1 {
		t.Fatalf("sync inbound: %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "GET", "/api/hosts", token, nil)
	if resp.Code != 200 {
		t.Fatalf("get hosts: %d %v", resp.Code, resp.Body)
	}
	hosts, ok := resp.Body["VLESS TCP REALITY"].([]interface{})
	if !ok || len(hosts) != 1 {
		t.Fatalf(`hosts["VLESS TCP REALITY"] = %v, want exactly one default host`, resp.Body["VLESS TCP REALITY"])
	}

	// Re-syncing the same tag must not create a second default host.
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VLESS TCP REALITY", "protocol": "vless"}})
	resp = doRequest(t, router, "GET", "/api/hosts", token, nil)
	hosts = resp.Body["VLESS TCP REALITY"].([]interface{})
	if len(hosts) != 1 {
		t.Errorf("after re-sync, got %d hosts, want still 1 (no duplicate default host)", len(hosts))
	}
}

func TestPutHostsRejectsInvalidFragmentSetting(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})

	resp := doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VMess TCP": []map[string]interface{}{
			{"remark": "r", "address": "a", "fragment_setting": "not-a-valid-fragment"},
		},
	})
	if resp.Code != 422 {
		t.Fatalf("expected 422 for invalid fragment_setting, got %d %v", resp.Code, resp.Body)
	}
}

func TestPutHostsRejectsUnbalancedBraces(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})

	resp := doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VMess TCP": []map[string]interface{}{
			{"remark": "{USERNAME", "address": "a"},
		},
	})
	if resp.Code != 422 {
		t.Fatalf("expected 422 for unbalanced braces in remark, got %d %v", resp.Code, resp.Body)
	}
}

func TestPutHostsFullReplace(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})

	// The sync above already created one default host; a PUT must fully
	// replace it, not append.
	resp := doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"VMess TCP": []map[string]interface{}{
			{"remark": "Only Host", "address": "example.com", "security": "tls"},
		},
	})
	if resp.Code != 200 {
		t.Fatalf("put hosts: %d %v", resp.Code, resp.Body)
	}
	hosts := resp.Body["VMess TCP"].([]interface{})
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts after replace, want exactly 1", len(hosts))
	}
	if hosts[0].(map[string]interface{})["remark"] != "Only Host" {
		t.Errorf("remark = %v, want the replaced value, not the old default", hosts[0])
	}
}

func TestUserTemplateCRUD(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})

	resp := doRequest(t, router, "POST", "/api/user_template", token, map[string]interface{}{
		"name": "Basic Plan", "data_limit": 10_000_000_000, "expire_duration": 2592000,
		"inbounds": map[string]interface{}{"vmess": []string{"VMess TCP"}},
	})
	if resp.Code != 200 {
		t.Fatalf("create template: %d %v", resp.Code, resp.Body)
	}
	id := int(resp.Body["id"].(float64))

	resp = doRequest(t, router, "GET", pathf("/api/user_template/%d", id), token, nil)
	if resp.Code != 200 || resp.Body["name"] != "Basic Plan" {
		t.Fatalf("get template: %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "PUT", pathf("/api/user_template/%d", id), token, map[string]interface{}{
		"name": "Basic Plan v2", "data_limit": 20_000_000_000, "expire_duration": 2592000,
		"inbounds": map[string]interface{}{"vmess": []string{"VMess TCP"}},
	})
	if resp.Code != 200 || resp.Body["name"] != "Basic Plan v2" {
		t.Fatalf("update template: %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "DELETE", pathf("/api/user_template/%d", id), token, nil)
	if resp.Code != 200 {
		t.Fatalf("delete template: %d %v", resp.Code, resp.Body)
	}
	resp = doRequest(t, router, "GET", pathf("/api/user_template/%d", id), token, nil)
	if resp.Code != 404 {
		t.Fatalf("expected 404 after delete, got %d %v", resp.Code, resp.Body)
	}
}

func TestUserTemplateDuplicateNameRejected(t *testing.T) {
	router, token := newTestRouter(t)
	body := map[string]interface{}{"name": "Dup", "data_limit": 1, "expire_duration": 1}
	doRequest(t, router, "POST", "/api/user_template", token, body)
	resp := doRequest(t, router, "POST", "/api/user_template", token, body)
	if resp.Code != 409 {
		t.Fatalf("expected 409 for duplicate template name, got %d %v", resp.Code, resp.Body)
	}
}

func pathf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}
