package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
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

	// 2037 - the latest a unix timestamp can be and still fit users.expire's
	// 32-bit INTEGER column (4102444800, "2100", silently wrapped negative
	// before expireFitsColumn started rejecting it).
	future := int64(2100000000)
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

// TestGetUserExposesNestedAdminAndSubscriptionMetadata proves the wire-shape
// fix for external Marzban-standard bots (Mirza-bot and similar - see the
// go-rewrite-gateway/PasarGuard research): GET /api/user/{username} must
// return a full nested `admin` object (id/username/is_sudo/telegram_id/
// discord_webhook/users_usage), not the flat `admin_username` string this
// used to be, plus `sub_updated_at`/`sub_last_user_agent`/`emergency_used_at`
// - all three already tracked in the users table, just never surfaced here.
func TestGetUserExposesNestedAdminAndSubscriptionMetadata(t *testing.T) {
	router, sudoToken := newTestRouter(t)
	pool := testPool(t)
	ctx := context.Background()

	adminResp := doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{
		"username": "wireshape_reseller", "password": "SomePassword123", "is_sudo": false,
		"telegram_id": 555, "discord_webhook": "https://discord.com/api/webhooks/1/abc",
	})
	if adminResp.Code != http.StatusOK {
		t.Fatalf("create admin: %d %v", adminResp.Code, adminResp.Body)
	}
	adminToken := loginAs(t, router, "wireshape_reseller", "SomePassword123")

	doRequest(t, router, "POST", "/api/inbounds/sync", sudoToken, []map[string]interface{}{{"tag": "Wireshape VLESS", "protocol": "vless"}})
	createResp := doRequest(t, router, "POST", "/api/user", adminToken, map[string]interface{}{
		"username": "wireshape_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create user: %d %v", createResp.Code, createResp.Body)
	}

	if _, err := pool.Exec(ctx,
		"UPDATE users SET sub_updated_at = now(), sub_last_user_agent = $2, emergency_used_at = now() WHERE username = $1",
		"wireshape_user", "v2rayNG/1.8.0",
	); err != nil {
		t.Fatalf("backdate subscription/emergency columns: %v", err)
	}

	got := doRequest(t, router, "GET", "/api/user/wireshape_user", sudoToken, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get user: %d %v", got.Code, got.Body)
	}

	admin, ok := got.Body["admin"].(map[string]interface{})
	if !ok {
		t.Fatalf("admin field missing or not an object: %v", got.Body["admin"])
	}
	if admin["username"] != "wireshape_reseller" {
		t.Errorf("admin.username = %v, want wireshape_reseller", admin["username"])
	}
	if admin["is_sudo"] != false {
		t.Errorf("admin.is_sudo = %v, want false", admin["is_sudo"])
	}
	if admin["telegram_id"] != float64(555) {
		t.Errorf("admin.telegram_id = %v, want 555", admin["telegram_id"])
	}
	if admin["discord_webhook"] != "https://discord.com/api/webhooks/1/abc" {
		t.Errorf("admin.discord_webhook = %v, want the created webhook URL", admin["discord_webhook"])
	}
	if _, hasFlatField := got.Body["admin_username"]; hasFlatField {
		t.Errorf("admin_username still present in the response - should be fully replaced by the nested admin object, got %v", got.Body["admin_username"])
	}

	if got.Body["sub_updated_at"] == nil {
		t.Error("sub_updated_at = nil, want a timestamp")
	}
	if got.Body["sub_last_user_agent"] != "v2rayNG/1.8.0" {
		t.Errorf("sub_last_user_agent = %v, want v2rayNG/1.8.0", got.Body["sub_last_user_agent"])
	}
	if got.Body["emergency_used_at"] == nil {
		t.Error("emergency_used_at = nil, want a timestamp")
	}

	// The list endpoint (GET /api/users) shares the exact same buildUserResponses
	// path - confirm the nested admin object is present there too, not just
	// on the single-user GET.
	list := doRequest(t, router, "GET", "/api/users", sudoToken, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list users: %d %v", list.Code, list.Body)
	}
	users := list.Body["users"].([]interface{})
	found := false
	for _, raw := range users {
		u := raw.(map[string]interface{})
		if u["username"] != "wireshape_user" {
			continue
		}
		found = true
		listAdmin, ok := u["admin"].(map[string]interface{})
		if !ok || listAdmin["username"] != "wireshape_reseller" {
			t.Errorf("list response admin = %v, want nested object with username wireshape_reseller", u["admin"])
		}
	}
	if !found {
		t.Fatalf("wireshape_user not found in GET /api/users")
	}
}

// TestListUsersTotalIsRealCountNotPageSize is a regression test for a real
// bug found via live stress-testing: GET /api/users reported "total" as
// len(page) - with 222 real users in the DB, GET /api/users?limit=1 returned
// "total":1. A paginating client (the dashboard, or an external tool like
// Mirza-bot) computing page counts from that field would be completely
// wrong the moment it passed an explicit limit smaller than the real total.
func TestListUsersTotalIsRealCountNotPageSize(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "Pagination VLESS", "protocol": "vless"}})

	const realUserCount = 7
	for i := 0; i < realUserCount; i++ {
		resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
			"username": fmt.Sprintf("pagination_user_%d", i), "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create user %d: %d %v", i, resp.Code, resp.Body)
		}
	}

	for _, limit := range []int{1, 2, realUserCount} {
		resp := doRequest(t, router, "GET", fmt.Sprintf("/api/users?limit=%d", limit), token, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("list users (limit=%d): %d %v", limit, resp.Code, resp.Body)
		}
		total, ok := resp.Body["total"].(float64)
		if !ok || int(total) != realUserCount {
			t.Errorf("limit=%d: total = %v, want the real count %d regardless of page size", limit, resp.Body["total"], realUserCount)
		}
		users, _ := resp.Body["users"].([]interface{})
		wantPageLen := limit
		if wantPageLen > realUserCount {
			wantPageLen = realUserCount
		}
		if len(users) != wantPageLen {
			t.Errorf("limit=%d: page has %d users, want %d", limit, len(users), wantPageLen)
		}
	}
}

// TestListUsersSortMatchesOldDashboardOptions verifies each of the 5 sort
// values the old dashboard's dropdown sends (see users.sql's ListUsers doc
// comment), plus that an unrecognized/missing value falls back to the same
// default the dashboard itself defaults to ("-created_at", newest first) -
// not just whatever the SQL CASE expression's tiebreaker happens to do.
func TestListUsersSortMatchesOldDashboardOptions(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "Sort VLESS", "protocol": "vless"}})

	type spec struct {
		username    string
		usedTraffic int64
		expire      int64
	}
	// Created in this exact order, so id and created_at both increase
	// alice -> bob -> carol - lets created_at-based sorts and id-based
	// sorts be checked with the same fixture.
	// Every expire here must fit in users.expire's 32-bit INTEGER column.
	// The original fixture used 3000000000, which does not - it wrapped to
	// a negative timestamp, which silently made alice sort FIRST and made
	// this test look flaky for a long time. See expireFitsColumn: the API
	// now rejects such a value outright instead of wrapping it.
	specs := []spec{
		{"alice", 300, 2000000000},
		{"bob", 100, 1000000000},
		{"carol", 200, 1500000000},
	}
	for _, s := range specs {
		resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
			"username": s.username, "expire": s.expire,
			"proxies": map[string]interface{}{"vless": map[string]interface{}{}},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create user %s: %d %v", s.username, resp.Code, resp.Body)
		}
		// used_traffic isn't writable through PUT /api/user (it's tracked
		// via node reports, see nodereport.go) - set it directly, same as
		// store_test.go does for fields no HTTP endpoint exposes.
		if _, err := handler.store.Pool.Exec(context.Background(),
			"UPDATE users SET used_traffic = $1 WHERE username = $2", s.usedTraffic, s.username); err != nil {
			t.Fatalf("seed used_traffic for %s: %v", s.username, err)
		}
	}

	usernamesInOrder := func(sort string) []string {
		t.Helper()
		url := "/api/users"
		if sort != "" {
			url += "?sort=" + sort
		}
		resp := doRequest(t, router, "GET", url, token, nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("list users (sort=%q): %d %v", sort, resp.Code, resp.Body)
		}
		users, _ := resp.Body["users"].([]interface{})
		out := make([]string, 0, len(users))
		for _, u := range users {
			out = append(out, u.(map[string]interface{})["username"].(string))
		}
		return out
	}

	cases := []struct {
		sort string
		want []string
	}{
		{"-created_at", []string{"carol", "bob", "alice"}}, // newest first
		{"created_at", []string{"alice", "bob", "carol"}},  // oldest first
		{"username", []string{"alice", "bob", "carol"}},    // A-Z
		{"-used_traffic", []string{"alice", "carol", "bob"}},
		{"expire", []string{"bob", "carol", "alice"}}, // soonest first
		{"", []string{"carol", "bob", "alice"}},        // missing -> default newest-first
		{"not-a-real-option", []string{"carol", "bob", "alice"}}, // unrecognized -> same default
	}
	for _, c := range cases {
		if got := usernamesInOrder(c.sort); !reflect.DeepEqual(got, c.want) {
			t.Errorf("sort=%q: order = %v, want %v", c.sort, got, c.want)
		}
	}
}

// TestCreateUserRejectsOutOfRangeExpire is the regression test for what
// looked like a flaky sort test for a long time: users.expire is a 32-bit
// INTEGER, and an oversized value used to be cast with a plain int32()
// conversion, which wraps instead of failing. The user came back 200 OK
// with an expire far in the NEGATIVE past - already expired, silently.
func TestCreateUserRejectsOutOfRangeExpire(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "Expire VLESS", "protocol": "vless"}})

	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "overflow_user", "expire": 3000000000,
		"proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create with an out-of-range expire = %d %v, want 422", resp.Code, resp.Body)
	}

	// An in-range value is still accepted and round-trips exactly.
	ok := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "inrange_user", "expire": 2000000000,
		"proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("create with an in-range expire: %d %v", ok.Code, ok.Body)
	}
	if expire, _ := ok.Body["expire"].(float64); int64(expire) != 2000000000 {
		t.Errorf("expire = %v, want 2000000000 stored verbatim", ok.Body["expire"])
	}

	// The same guard applies on edit, where the wrap would silently expire
	// a live customer.
	edit := doRequest(t, router, "PUT", "/api/user/inrange_user", token, map[string]interface{}{"expire": 4000000000})
	if edit.Code != http.StatusUnprocessableEntity {
		t.Errorf("modify with an out-of-range expire = %d %v, want 422", edit.Code, edit.Body)
	}
}

// TestDeleteUserSucceedsAfterAUsageReset is a regression test for a real
// bug found via a live compatibility test against a real, unmodified
// reseller bot (WizWiz): every user-owned table except user_usage_logs was
// promoted to a real ON DELETE CASCADE FK (see 00001_init_schema.sql's own
// history) - that one table was left on Postgres's default NO ACTION, so
// deleting any user who had ever had their traffic reset (inserting a
// user_usage_logs row) failed outright with a plain 500, not the normal
// success response every other user could get.
func TestDeleteUserSucceedsAfterAUsageReset(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "Delete VLESS", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "delete_after_reset_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	resetResp := doRequest(t, router, "POST", "/api/user/delete_after_reset_user/reset", token, nil)
	if resetResp.Code != http.StatusOK {
		t.Fatalf("reset user: %d %v", resetResp.Code, resetResp.Body)
	}

	delResp := doRequest(t, router, "DELETE", "/api/user/delete_after_reset_user", token, nil)
	if delResp.Code != http.StatusOK {
		t.Fatalf("delete user after a reset: %d %v, want 200 - a user_usage_logs row must not block deletion", delResp.Code, delResp.Body)
	}
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
