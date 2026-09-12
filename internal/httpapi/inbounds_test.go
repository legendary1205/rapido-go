package httpapi

import (
	"encoding/json"
	"net/http"
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

// TestNewInboundsGetDistinctHostPriorities is a regression test for a real
// bug: createDefaultHost never set a priority at all, so every
// auto-created default host landed on the hosts.priority column's bare
// default (0) - identical to every other one. The Hosts page's up/down
// reorder buttons swap two hosts' priority values, so swapping two hosts
// that are both already at 0 is a real no-op (nothing to compare), making
// reordering look broken even though the swap logic itself was correct.
// Two inbounds synced in the same request must get their own default
// hosts on two distinct, increasing priorities.
func TestNewInboundsGetDistinctHostPriorities(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{
		{"tag": "Priority A", "protocol": "vless"},
	})
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{
		{"tag": "Priority B", "protocol": "vless"},
	})

	resp := doRequest(t, router, "GET", "/api/hosts", token, nil)
	if resp.Code != 200 {
		t.Fatalf("get hosts: %d %v", resp.Code, resp.Body)
	}
	hostsA, _ := resp.Body["Priority A"].([]any)
	hostsB, _ := resp.Body["Priority B"].([]any)
	if len(hostsA) != 1 || len(hostsB) != 1 {
		t.Fatalf("expected one default host per tag, got A=%v B=%v", hostsA, hostsB)
	}
	priorityA := hostsA[0].(map[string]any)["priority"].(float64)
	priorityB := hostsB[0].(map[string]any)["priority"].(float64)
	if priorityA == priorityB {
		t.Errorf("Priority A and Priority B's default hosts both got priority %v, want two distinct values", priorityA)
	}
	if priorityB <= priorityA {
		t.Errorf("second inbound's host priority (%v) should be greater than the first's (%v)", priorityB, priorityA)
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
	for _, tag := range inboundTagsOf(t, getInboundsResp, "vless") {
		if tag == "To Delete" {
			t.Errorf("deleted inbound tag still present in GET /api/inbounds: %v", getInboundsResp.Body)
		}
	}
}

// inboundTagsOf pulls the tags for one protocol out of GET /api/inbounds,
// whose entries are ProxyInbound objects ({tag, protocol, network, tls,
// port}) exactly as the real panel returns - see handleListInbounds.
func inboundTagsOf(t *testing.T, resp apiResponse, protocol string) []string {
	t.Helper()
	entries, ok := resp.Body[protocol].([]interface{})
	if !ok {
		return nil
	}
	tags := make([]string, 0, len(entries))
	for _, e := range entries {
		obj, ok := e.(map[string]interface{})
		if !ok {
			t.Fatalf("GET /api/inbounds entry is %T, want an object with a tag field: %v", e, e)
		}
		tags = append(tags, obj["tag"].(string))
	}
	return tags
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

// userProxyProtocols fetches a user and returns the set of protocol keys
// present in its "proxies" object.
func userProxyProtocols(t *testing.T, router http.Handler, token, username string) map[string]bool {
	t.Helper()
	resp := doRequest(t, router, "GET", "/api/user/"+username, token, nil)
	if resp.Code != 200 {
		t.Fatalf("get user %s: %d %v", username, resp.Code, resp.Body)
	}
	proxies, _ := resp.Body["proxies"].(map[string]interface{})
	out := make(map[string]bool, len(proxies))
	for k := range proxies {
		out[k] = true
	}
	return out
}

// TestDeletingLastInboundOfAProtocolPrunesThatProtocolsProxies is a
// regression test found by the user comparing a real migration's data
// against the source panel: a protocol dropped from the live Xray/Core
// Config (here, vmess going from "has one inbound" to "has none") must not
// leave every affected user still carrying a vmess proxy credential nobody
// can ever serve again.
func TestDeletingLastInboundOfAProtocolPrunesThatProtocolsProxies(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{
		{"tag": "VLESS-1", "protocol": "vless"},
		{"tag": "VMess-only", "protocol": "vmess"},
	})
	create := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "multi-proto-user",
		"proxies":  map[string]interface{}{"vless": map[string]interface{}{}, "vmess": map[string]interface{}{}},
	})
	if create.Code != 200 {
		t.Fatalf("create user: %d %v", create.Code, create.Body)
	}
	if got := userProxyProtocols(t, router, token, "multi-proto-user"); !got["vless"] || !got["vmess"] {
		t.Fatalf("user should start with both vless and vmess proxies, got %v", got)
	}

	del := doRequest(t, router, "DELETE", "/api/inbounds/VMess-only", token, nil)
	if del.Code != 200 {
		t.Fatalf("delete VMess-only: %d %v", del.Code, del.Body)
	}
	if n, _ := del.Body["orphaned_proxies_removed"].(float64); n != 1 {
		t.Errorf("orphaned_proxies_removed = %v, want 1", del.Body["orphaned_proxies_removed"])
	}

	got := userProxyProtocols(t, router, token, "multi-proto-user")
	if got["vmess"] {
		t.Errorf("vmess proxy should have been pruned once its only inbound was deleted, still present: %v", got)
	}
	if !got["vless"] {
		t.Errorf("vless proxy should be untouched (vless still has an inbound), got %v", got)
	}
}

// TestDeletingOneOfSeveralInboundsOfTheSameProtocolPrunesNothing makes sure
// the prune is genuinely per-protocol, not per-tag: removing one of two
// vless inbounds must not touch anyone's vless proxy, since vless itself
// still has a surviving inbound.
func TestDeletingOneOfSeveralInboundsOfTheSameProtocolPrunesNothing(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{
		{"tag": "VLESS-A", "protocol": "vless"},
		{"tag": "VLESS-B", "protocol": "vless"},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "vless-user",
		"proxies":  map[string]interface{}{"vless": map[string]interface{}{}},
	})

	del := doRequest(t, router, "DELETE", "/api/inbounds/VLESS-A", token, nil)
	if del.Code != 200 {
		t.Fatalf("delete VLESS-A: %d %v", del.Code, del.Body)
	}
	if n, _ := del.Body["orphaned_proxies_removed"].(float64); n != 0 {
		t.Errorf("orphaned_proxies_removed = %v, want 0 (VLESS-B still serves vless)", del.Body["orphaned_proxies_removed"])
	}
	if got := userProxyProtocols(t, router, token, "vless-user"); !got["vless"] {
		t.Errorf("vless proxy should be untouched, got %v", got)
	}
}

// TestPruneOrphanedProxiesNeverRunsWhenNoInboundsRemainAtAll is the safety
// guard test: PruneOrphanedProxies's SQL is `type NOT IN (SELECT protocol
// FROM inbounds)`, and an empty inbounds table makes that NOT IN match
// every row - the exact opposite of "orphaned". Deleting every inbound one
// at a time (a real workflow: clearing everything before a fresh import)
// must never wipe every user's proxies as a side effect.
func TestPruneOrphanedProxiesNeverRunsWhenNoInboundsRemainAtAll(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "Only VLESS", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "sole-user",
		"proxies":  map[string]interface{}{"vless": map[string]interface{}{}},
	})

	del := doRequest(t, router, "DELETE", "/api/inbounds/"+url.PathEscape("Only VLESS"), token, nil)
	if del.Code != 200 {
		t.Fatalf("delete Only VLESS: %d %v", del.Code, del.Body)
	}
	if n, _ := del.Body["orphaned_proxies_removed"].(float64); n != 0 {
		t.Errorf("orphaned_proxies_removed = %v, want 0 - zero inbounds must skip pruning entirely", del.Body["orphaned_proxies_removed"])
	}
	if got := userProxyProtocols(t, router, token, "sole-user"); !got["vless"] {
		t.Errorf("vless proxy must survive deleting the last inbound - pruning must refuse to run with zero inbounds left, got %v", got)
	}
}
