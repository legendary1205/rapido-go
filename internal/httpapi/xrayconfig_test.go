package httpapi

import (
	"net/http"
	"testing"
)

func TestGetXrayConfigCombinesCoreConfigAndInbounds(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "Xray VLESS", "protocol": "vless"},
	})

	resp := doRequest(t, router, "GET", "/api/settings/xray-config", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get xray config: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["log_level"] != "warn" {
		t.Errorf("log_level = %v, want warn", resp.Body["log_level"])
	}
	inbounds, ok := resp.Body["inbounds"].([]interface{})
	if !ok || len(inbounds) != 1 {
		t.Fatalf("inbounds = %v, want exactly the 1 synced inbound", resp.Body["inbounds"])
	}
	in := inbounds[0].(map[string]interface{})
	if in["tag"] != "Xray VLESS" || in["protocol"] != "vless" {
		t.Errorf("inbound = %v, want tag=Xray VLESS protocol=vless", in)
	}
}

// TestUpdateXrayConfigAcceptsANewInboundAndARuleTargetingItInOneRequest is
// the whole point of this endpoint existing rather than two separate PUTs:
// the OLD /api/settings/core-config on its own rejects a routing rule that
// targets an inbound tag not already in the database (see
// TestUpdateCoreConfigRejectsRuleTargetingUnknownInbound) - an admin editing
// one combined JSON document naturally wants to add a brand-new inbound and
// a rule that routes its traffic in the same edit. This must succeed.
func TestUpdateXrayConfigAcceptsANewInboundAndARuleTargetingItInOneRequest(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "PUT", "/api/settings/xray-config", token, map[string]interface{}{
		"log_level":     "warn",
		"sniff_enabled": true,
		"inbounds": []map[string]interface{}{
			{"tag": "Brand New VLESS", "protocol": "vless"},
		},
		"routing_rules": []map[string]interface{}{
			{"inbound": []string{"Brand New VLESS"}, "outbound_tag": "block"},
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("put xray config: %d %v", resp.Code, resp.Body)
	}

	inbounds := resp.Body["inbounds"].([]interface{})
	if len(inbounds) != 1 || inbounds[0].(map[string]interface{})["tag"] != "Brand New VLESS" {
		t.Errorf("inbounds = %v, want exactly Brand New VLESS", inbounds)
	}
	rules := resp.Body["routing_rules"].([]interface{})
	if len(rules) != 1 {
		t.Fatalf("routing_rules = %v, want 1 entry", rules)
	}

	// The inbound must actually exist for real afterward, not just echoed
	// back in this one response - confirm via the plain inbounds list.
	listResp := doRequest(t, router, "GET", "/api/inbounds", token, nil)
	vlessTags, _ := listResp.Body["vless"].([]interface{})
	found := false
	for _, tag := range vlessTags {
		if tag == "Brand New VLESS" {
			found = true
		}
	}
	if !found {
		t.Errorf("Brand New VLESS not found in GET /api/inbounds: %v", listResp.Body)
	}
}

// TestUpdateXrayConfigRejectsBadShapeBeforeTouchingInbounds proves the
// cheap pre-check: a request with an inbound AND an invalid core-config
// value (bad log_level) must be rejected without creating the inbound at
// all - a plain typo in log_level shouldn't have the side effect of a new
// inbound (and its auto-created default host) silently appearing.
func TestUpdateXrayConfigRejectsBadShapeBeforeTouchingInbounds(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "PUT", "/api/settings/xray-config", token, map[string]interface{}{
		"log_level": "not-a-real-level",
		"inbounds": []map[string]interface{}{
			{"tag": "Should Not Exist", "protocol": "vless"},
		},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid log_level: %d, want 422: %v", resp.Code, resp.Body)
	}

	listResp := doRequest(t, router, "GET", "/api/inbounds", token, nil)
	if vlessTags, ok := listResp.Body["vless"]; ok && len(vlessTags.([]interface{})) > 0 {
		t.Errorf("inbound was created despite the request being rejected: %v", listResp.Body)
	}
}

// TestUpdateXrayConfigDeletesInboundsOmittedFromThePayload is the real
// regression test for the full-replace behavior handleUpdateXrayConfig
// deliberately has and POST /api/inbounds/sync deliberately doesn't (see
// that handler's own doc comment) - once the dashboard only has this one
// JSON editor for inbounds, omitting a tag from a PUT has to be how an
// admin removes it, since the old per-row Delete button is gone.
func TestUpdateXrayConfigDeletesInboundsOmittedFromThePayload(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "Keep Me", "protocol": "vless"},
		{"tag": "Remove Me", "protocol": "trojan"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"Keep Me":   []map[string]interface{}{{"remark": "k", "address": "1.2.3.4", "port": 443, "security": "none"}},
		"Remove Me": []map[string]interface{}{{"remark": "r", "address": "1.2.3.4", "port": 444, "security": "none"}},
	})
	// A user holding both protocols - the trojan side should be pruned once
	// "Remove Me" (trojan's only inbound) disappears from this Apply, the
	// same real gap the migration itself hit (see PruneOrphanedProxies).
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "both-proto-user",
		"proxies":  map[string]interface{}{"vless": map[string]interface{}{}, "trojan": map[string]interface{}{}},
	})

	resp := doRequest(t, router, "PUT", "/api/settings/xray-config", token, map[string]interface{}{
		"inbounds": []map[string]interface{}{{"tag": "Keep Me", "protocol": "vless"}},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("put xray config: %d %v", resp.Code, resp.Body)
	}
	inbounds := resp.Body["inbounds"].([]interface{})
	if len(inbounds) != 1 || inbounds[0].(map[string]interface{})["tag"] != "Keep Me" {
		t.Fatalf("inbounds = %v, want only Keep Me left", inbounds)
	}

	// Confirmed gone for real, not just absent from this one response, and
	// its host cascaded away with it (DeleteInboundByTag's own cascade).
	listResp := doRequest(t, router, "GET", "/api/inbounds", token, nil)
	trojanTags, _ := listResp.Body["trojan"].([]interface{})
	if len(trojanTags) != 0 {
		t.Errorf("Remove Me's protocol still has tags: %v", trojanTags)
	}
	hostsResp := doRequest(t, router, "GET", "/api/hosts", token, nil)
	if _, stillThere := hostsResp.Body["Remove Me"]; stillThere {
		t.Errorf("Remove Me's host survived the inbound deletion: %v", hostsResp.Body)
	}
	if keepHosts, ok := hostsResp.Body["Keep Me"].([]interface{}); !ok || len(keepHosts) != 1 {
		t.Errorf("Keep Me's own host was disturbed: %v", hostsResp.Body["Keep Me"])
	}

	userResp := doRequest(t, router, "GET", "/api/user/both-proto-user", token, nil)
	proxies, _ := userResp.Body["proxies"].(map[string]interface{})
	if _, stillHasTrojan := proxies["trojan"]; stillHasTrojan {
		t.Errorf("trojan proxy should have been pruned once its only inbound was removed via Apply, got %v", proxies)
	}
	if _, stillHasVless := proxies["vless"]; !stillHasVless {
		t.Errorf("vless proxy should be untouched, got %v", proxies)
	}
}

func TestUpdateXrayConfigRejectsUnknownProtocol(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "PUT", "/api/settings/xray-config", token, map[string]interface{}{
		"inbounds": []map[string]interface{}{{"tag": "Bad Protocol", "protocol": "not-a-real-protocol"}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown protocol: %d, want 422: %v", resp.Code, resp.Body)
	}
}
