package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

func TestGatewaySettingsGeneratesASecretOnFirstAccess(t *testing.T) {
	router, token := newTestRouter(t)

	first := doRequest(t, router, "GET", "/api/settings/gateway", token, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("get gateway settings: %d %v", first.Code, first.Body)
	}
	secret1, _ := first.Body["secret"].(string)
	if secret1 == "" {
		t.Fatal("expected a real secret to be generated on first access, got empty string")
	}

	second := doRequest(t, router, "GET", "/api/settings/gateway", token, nil)
	secret2, _ := second.Body["secret"].(string)
	if secret2 != secret1 {
		t.Errorf("secret changed between two plain GETs (%q -> %q), want stable until an explicit rotate", secret1, secret2)
	}
}

func TestGatewaySettingsRotateAndRename(t *testing.T) {
	router, token := newTestRouter(t)
	before := doRequest(t, router, "GET", "/api/settings/gateway", token, nil)
	oldSecret, _ := before.Body["secret"].(string)

	resp := doRequest(t, router, "PUT", "/api/settings/gateway", token, map[string]interface{}{
		"name": "EU Panel", "rotate_secret": true,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("update gateway settings: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["name"] != "EU Panel" {
		t.Errorf("name = %v, want 'EU Panel'", resp.Body["name"])
	}
	newSecret, _ := resp.Body["secret"].(string)
	if newSecret == "" || newSecret == oldSecret {
		t.Errorf("secret after rotate = %q, want a new non-empty value (was %q)", newSecret, oldSecret)
	}
}

func TestGatewayPeerCRUDAndTestConnection(t *testing.T) {
	router, token := newTestRouter(t)

	created := doRequest(t, router, "POST", "/api/settings/gateway/peers", token, map[string]interface{}{
		"name": "Peer One", "base_url": "http://127.0.0.1:9", "secret": "whatever-secret",
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create peer: %d %v", created.Code, created.Body)
	}
	if created.Body["name"] != "Peer One" || created.Body["enabled"] != true {
		t.Errorf("created peer = %v, want name=Peer One enabled=true (default)", created.Body)
	}
	id := int64(created.Body["id"].(float64))

	list := doRequest(t, router, "GET", "/api/settings/gateway/peers", token, nil)
	var peers []map[string]interface{}
	if err := json.Unmarshal(list.Raw, &peers); err != nil {
		t.Fatalf("decode peers list: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("peers list = %v, want exactly 1", peers)
	}

	updated := doRequest(t, router, "PUT", "/api/settings/gateway/peers/"+strconv.FormatInt(id, 10), token, map[string]interface{}{
		"name": "Peer One Renamed", "base_url": "http://127.0.0.1:9", "secret": "whatever-secret", "enabled": false,
	})
	if updated.Code != http.StatusOK || updated.Body["name"] != "Peer One Renamed" || updated.Body["enabled"] != false {
		t.Fatalf("update peer: %d %v", updated.Code, updated.Body)
	}

	// Port 9 (discard) on localhost: guaranteed to refuse the connection,
	// so this exercises the real failure path of a genuine outbound call
	// rather than a syntax check of the stored URL.
	test := doRequest(t, router, "POST", "/api/settings/gateway/peers/"+strconv.FormatInt(id, 10)+"/test", token, nil)
	if test.Code != http.StatusOK {
		t.Fatalf("test peer connection: %d %v", test.Code, test.Body)
	}
	if test.Body["ok"] != false {
		t.Errorf("test connection against an unreachable peer = %v, want ok:false", test.Body)
	}

	del := doRequest(t, router, "DELETE", "/api/settings/gateway/peers/"+strconv.FormatInt(id, 10), token, nil)
	if del.Code != http.StatusOK {
		t.Fatalf("delete peer: %d %v", del.Code, del.Body)
	}
	listAfter := doRequest(t, router, "GET", "/api/settings/gateway/peers", token, nil)
	var peersAfter []map[string]interface{}
	json.Unmarshal(listAfter.Raw, &peersAfter)
	if len(peersAfter) != 0 {
		t.Errorf("peers list after delete = %v, want empty", peersAfter)
	}
}

// TestGatewayPingRequiresTheRealSecret proves requireGatewaySecret itself:
// doRequest's token parameter becomes the Authorization: Bearer header
// (same helper nodereport_test.go uses to authenticate as a node), so
// passing a wrong value and the real generated secret exercises the exact
// header-parsing/comparison path a real peer's HTTP call would hit. The
// live cross-process proof (two real listening binaries on the test
// server, one genuinely calling the other over the network) is done
// manually per the Gateway plan - this is the fast, hermetic version of
// the same check.
func TestGatewayPingRequiresTheRealSecret(t *testing.T) {
	router, token := newTestRouter(t)

	settings := doRequest(t, router, "GET", "/api/settings/gateway", token, nil)
	secret, _ := settings.Body["secret"].(string)
	if secret == "" {
		t.Fatal("no gateway secret was generated")
	}

	badReq := doRequest(t, router, "GET", "/api/internal/gateway/ping", "wrong-secret", nil)
	if badReq.Code != http.StatusUnauthorized {
		t.Fatalf("ping with wrong secret: %d %v, want 401", badReq.Code, badReq.Body)
	}

	goodReq := doRequest(t, router, "GET", "/api/internal/gateway/ping", secret, nil)
	if goodReq.Code != http.StatusOK {
		t.Fatalf("ping with real secret: %d %v, want 200", goodReq.Code, goodReq.Body)
	}
}
