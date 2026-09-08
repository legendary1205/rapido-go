package httpapi

import (
	"net/http"
	"testing"
)

// TestGatewaySyncCreatesAndUpdatesAReplica exercises the receiving side
// (POST /api/internal/gateway/users/sync) directly, in-process - the real
// cross-network proof (two independent panel processes, two databases,
// a real handshake against a real port using the synced credential) is
// done manually against the test server per the Gateway plan, same
// division of labor as sub-phase 1's own test file.
func TestGatewaySyncCreatesAndUpdatesAReplica(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "Sync VLESS", "protocol": "vless"}})

	secretResp := doRequest(t, router, "GET", "/api/settings/gateway", token, nil)
	secret, _ := secretResp.Body["secret"].(string)

	create := doRequest(t, router, "POST", "/api/internal/gateway/users/sync", secret, map[string]interface{}{
		"origin_panel_name":         "Origin Panel",
		"username":                  "replica_alice",
		"status":                    "active",
		"data_limit":                5000000000,
		"data_limit_reset_strategy": "no_reset",
		"proxies": map[string]interface{}{
			"vless": map[string]string{"id": "11111111-1111-1111-1111-111111111111"},
		},
	})
	if create.Code != http.StatusOK || create.Body["ok"] != true {
		t.Fatalf("create replica: %d %v", create.Code, create.Body)
	}

	got := doRequest(t, router, "GET", "/api/user/replica_alice", token, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get replica: %d %v", got.Code, got.Body)
	}
	if got.Body["status"] != "active" {
		t.Errorf("replica status = %v, want active", got.Body["status"])
	}
	proxies, _ := got.Body["proxies"].(map[string]interface{})
	vless, _ := proxies["vless"].(map[string]interface{})
	if vless["id"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("replica vless id = %v, want the synced uuid", vless["id"])
	}

	// A direct local edit must be rejected - this user is managed by the
	// origin panel, not this one.
	editAttempt := doRequest(t, router, "PUT", "/api/user/replica_alice", token, map[string]interface{}{
		"status": "disabled",
	})
	if editAttempt.Code != http.StatusConflict {
		t.Errorf("direct edit of a replica: %d %v, want 409", editAttempt.Code, editAttempt.Body)
	}

	// A second sync push (status change) updates it in place.
	update := doRequest(t, router, "POST", "/api/internal/gateway/users/sync", secret, map[string]interface{}{
		"origin_panel_name": "Origin Panel",
		"username":          "replica_alice",
		"status":            "disabled",
		"proxies": map[string]interface{}{
			"vless": map[string]string{"id": "11111111-1111-1111-1111-111111111111"},
		},
	})
	if update.Code != http.StatusOK {
		t.Fatalf("update replica: %d %v", update.Code, update.Body)
	}
	gotAfter := doRequest(t, router, "GET", "/api/user/replica_alice", token, nil)
	if gotAfter.Body["status"] != "disabled" {
		t.Errorf("replica status after update = %v, want disabled", gotAfter.Body["status"])
	}

	// Deleting a replica via a sync push actually removes it.
	del := doRequest(t, router, "POST", "/api/internal/gateway/users/sync", secret, map[string]interface{}{
		"username": "replica_alice", "deleted": true,
	})
	if del.Code != http.StatusOK {
		t.Fatalf("delete replica: %d %v", del.Code, del.Body)
	}
	gone := doRequest(t, router, "GET", "/api/user/replica_alice", token, nil)
	if gone.Code != http.StatusNotFound {
		t.Errorf("replica after delete sync: %d, want 404", gone.Code)
	}
}

// TestGatewaySyncNeverOverwritesARealLocalUser is the safety property the
// whole design hinges on: a genuine local user (created through the
// normal admin API, never touched by a sync push) must never be silently
// clobbered just because a peer happens to push a user with the same
// username.
func TestGatewaySyncNeverOverwritesARealLocalUser(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "Sync VLESS 2", "protocol": "vless"}})

	created := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "real_bob",
		"proxies": map[string]interface{}{
			"vless": map[string]string{"id": "22222222-2222-2222-2222-222222222222"},
		},
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create real user: %d %v", created.Code, created.Body)
	}

	secretResp := doRequest(t, router, "GET", "/api/settings/gateway", token, nil)
	secret, _ := secretResp.Body["secret"].(string)

	collision := doRequest(t, router, "POST", "/api/internal/gateway/users/sync", secret, map[string]interface{}{
		"origin_panel_name": "Some Other Panel",
		"username":          "real_bob",
		"status":            "active",
		"proxies": map[string]interface{}{
			"vless": map[string]string{"id": "99999999-9999-9999-9999-999999999999"},
		},
	})
	if collision.Code != http.StatusConflict {
		t.Fatalf("sync colliding with a real local user: %d %v, want 409", collision.Code, collision.Body)
	}

	// Confirm real_bob's real uuid was NOT overwritten.
	got := doRequest(t, router, "GET", "/api/user/real_bob", token, nil)
	proxies, _ := got.Body["proxies"].(map[string]interface{})
	vless, _ := proxies["vless"].(map[string]interface{})
	if vless["id"] != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("real_bob's proxy was overwritten: %v", vless)
	}

	deleteCollision := doRequest(t, router, "POST", "/api/internal/gateway/users/sync", secret, map[string]interface{}{
		"username": "real_bob", "deleted": true,
	})
	if deleteCollision.Code != http.StatusConflict {
		t.Errorf("delete-sync colliding with a real local user: %d %v, want 409", deleteCollision.Code, deleteCollision.Body)
	}
}
