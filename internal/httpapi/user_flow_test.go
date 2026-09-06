package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCreateUserDefaultsStatusToActive(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	if resp.Code != 200 {
		t.Fatalf("sync inbounds: %d %v", resp.Code, resp.Body)
	}

	resp = doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "alice_test",
		"proxies":  map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["status"] != "active" {
		t.Errorf("status = %v, want active (Python leaves status None on omission - the Go rewrite defaults it explicitly, see report)", resp.Body["status"])
	}
	inbounds := resp.Body["inbounds"].(map[string]interface{})
	vmessTags := toStringSlice(inbounds["vmess"])
	if len(vmessTags) != 1 || vmessTags[0] != "VMess TCP" {
		t.Errorf("inbounds[vmess] = %v, want auto-filled with the one known vmess inbound", vmessTags)
	}
	excluded := resp.Body["excluded_inbounds"].(map[string]interface{})
	if len(toStringSlice(excluded["vmess"])) != 0 {
		t.Errorf("excluded_inbounds[vmess] = %v, want empty since the single known inbound was included", excluded["vmess"])
	}

	proxies := resp.Body["proxies"].(map[string]interface{})
	vmess := proxies["vmess"].(map[string]interface{})
	if vmess["id"] == nil || vmess["id"] == "" {
		t.Error("proxies.vmess.id was not auto-generated")
	}
}

func TestCreateUserOnHoldRequiresExpireDuration(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})

	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "onhold_test",
		"status":   "on_hold",
		"proxies":  map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 422 {
		t.Fatalf("expected 422 for on_hold without on_hold_expire_duration, got %d %v", resp.Code, resp.Body)
	}
}

func TestCreateUserRejectsEmptyProxies(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "noproxy_test",
		"proxies":  map[string]interface{}{},
	})
	if resp.Code != 422 {
		t.Fatalf("expected 422 for empty proxies, got %d %v", resp.Code, resp.Body)
	}
}

func TestModifyUserDataLimitStatusTransitions(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "limit_test",
		"proxies":  map[string]interface{}{"vmess": map[string]interface{}{}},
	})

	// Raising the limit above used_traffic (0) keeps/reactivates status.
	resp := doRequest(t, router, "PUT", "/api/user/limit_test", token, map[string]interface{}{"data_limit": 1000})
	if resp.Code != 200 || resp.Body["status"] != "active" {
		t.Fatalf("expected active after raising data_limit, got %d %v", resp.Code, resp.Body)
	}

	// A data_limit of 0 means "unlimited" (falsy -> NULL), never limits.
	resp = doRequest(t, router, "PUT", "/api/user/limit_test", token, map[string]interface{}{"data_limit": 0})
	if resp.Code != 200 || resp.Body["data_limit"] != nil {
		t.Fatalf("expected data_limit cleared to null for 0, got %d %v", resp.Code, resp.Body)
	}
}

func TestModifyUserExpireStatusTransitions(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "expire_test",
		"proxies":  map[string]interface{}{"vmess": map[string]interface{}{}},
	})

	past := int64(1000000000) // 2001, long expired
	resp := doRequest(t, router, "PUT", "/api/user/expire_test", token, map[string]interface{}{"expire": past})
	if resp.Code != 200 || resp.Body["status"] != "expired" {
		t.Fatalf("expected status=expired for a past expire, got %d %v", resp.Code, resp.Body)
	}

	future := int64(4102444800) // 2100
	resp = doRequest(t, router, "PUT", "/api/user/expire_test", token, map[string]interface{}{"expire": future})
	if resp.Code != 200 || resp.Body["status"] != "active" {
		t.Fatalf("expected status=active after extending expire into the future, got %d %v", resp.Code, resp.Body)
	}
}

func TestModifyUserNextPlanUpsertAndClear(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "nextplan_test",
		"proxies":  map[string]interface{}{"vmess": map[string]interface{}{}},
	})

	resp := doRequest(t, router, "PUT", "/api/user/nextplan_test", token, map[string]interface{}{
		"next_plan": map[string]interface{}{"data_limit": 5000, "expire": 30, "add_remaining_traffic": true, "fire_on_either": false},
	})
	if resp.Code != 200 {
		t.Fatalf("set next_plan: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["next_plan"] == nil {
		t.Fatal("next_plan was not persisted")
	}

	// Omitting next_plan on a later PUT clears it - matches crud.update_user's
	// if/elif (Python can't distinguish "omitted" from "explicit null" either).
	resp = doRequest(t, router, "PUT", "/api/user/nextplan_test", token, map[string]interface{}{"note": "unrelated edit"})
	if resp.Code != 200 {
		t.Fatalf("clear next_plan via omission: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["next_plan"] != nil {
		t.Errorf("next_plan = %v, want cleared after a PUT that omits it", resp.Body["next_plan"])
	}
}

func TestUserOwnershipScoping(t *testing.T) {
	router, sudoToken := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", sudoToken, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})

	// Two non-sudo admins, each creating their own user.
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "owner-a", "password": "pw12345", "is_sudo": false})
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "owner-b", "password": "pw12345", "is_sudo": false})

	tokenA := loginAs(t, router, "owner-a", "pw12345")
	tokenB := loginAs(t, router, "owner-b", "pw12345")

	resp := doRequest(t, router, "POST", "/api/user", tokenA, map[string]interface{}{
		"username": "owned_by_a",
		"proxies":  map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user as owner-a: %d %v", resp.Code, resp.Body)
	}

	// owner-b must not be able to see owner-a's user.
	resp = doRequest(t, router, "GET", "/api/user/owned_by_a", tokenB, nil)
	if resp.Code != 403 {
		t.Fatalf("expected 403 for cross-admin access, got %d %v", resp.Code, resp.Body)
	}

	// sudo can see it regardless.
	resp = doRequest(t, router, "GET", "/api/user/owned_by_a", sudoToken, nil)
	if resp.Code != 200 {
		t.Fatalf("expected sudo to see any user, got %d %v", resp.Code, resp.Body)
	}

	// owner-a's own user list must not include owner-b's users at all
	// (there are none here, but the filter itself must be admin-scoped).
	resp = doRequest(t, router, "GET", "/api/users", tokenA, nil)
	if resp.Code != 200 {
		t.Fatalf("list users as owner-a: %d %v", resp.Code, resp.Body)
	}
}

// loginAs performs a real form-encoded POST /api/admin/token, matching the
// actual login contract, and returns the issued access token.
func loginAs(t *testing.T, router http.Handler, username, password string) string {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login as %s failed: %d %s", username, rec.Code, rec.Body.String())
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	return body.AccessToken
}

func toStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		out = append(out, x.(string))
	}
	return out
}
