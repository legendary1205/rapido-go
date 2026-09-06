package httpapi

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSystemStatsCountsRealUsersByStatus(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})

	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "sys_active_1", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "sys_active_2", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "sys_onhold_1", "status": "on_hold", "on_hold_expire_duration": 86400,
		"proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})

	resp := doRequest(t, router, "GET", "/api/system", token, nil)
	if resp.Code != 200 {
		t.Fatalf("get system stats: %d %v", resp.Code, resp.Body)
	}
	if got := resp.Body["total_user"].(float64); got != 3 {
		t.Errorf("total_user = %v, want 3", got)
	}
	if got := resp.Body["users_active"].(float64); got != 2 {
		t.Errorf("users_active = %v, want 2", got)
	}
	if got := resp.Body["users_on_hold"].(float64); got != 1 {
		t.Errorf("users_on_hold = %v, want 1", got)
	}
	if got := resp.Body["online_users"].(float64); got != 0 {
		t.Errorf("online_users = %v, want 0 (none of this test's own users ever got a node report)", got)
	}
	// incoming_bandwidth/outgoing_bandwidth deliberately not asserted here:
	// they come from the `system` singleton row, which is fleet-wide and
	// shared across every test in this package (see
	// internal/httpapi/nodereport_test.go, which increments it for real) -
	// this test is about per-admin user-status counting, not bandwidth, so
	// it has no business asserting a value only some *other* test's writes
	// would determine.
}

func TestSystemStatsScopedToNonSudoAdmin(t *testing.T) {
	router, sudoToken := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", sudoToken, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "sys-owner-a", "password": "pw12345", "is_sudo": false})
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "sys-owner-b", "password": "pw12345", "is_sudo": false})
	tokenA := loginAs(t, router, "sys-owner-a", "pw12345")
	tokenB := loginAs(t, router, "sys-owner-b", "pw12345")

	doRequest(t, router, "POST", "/api/user", tokenA, map[string]interface{}{
		"username": "sys_owned_by_a", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	doRequest(t, router, "POST", "/api/user", tokenB, map[string]interface{}{
		"username": "sys_owned_by_b_1", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	doRequest(t, router, "POST", "/api/user", tokenB, map[string]interface{}{
		"username": "sys_owned_by_b_2", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})

	resp := doRequest(t, router, "GET", "/api/system", tokenA, nil)
	if resp.Code != 200 {
		t.Fatalf("get system stats as owner-a: %d %v", resp.Code, resp.Body)
	}
	if got := resp.Body["total_user"].(float64); got != 1 {
		t.Errorf("owner-a's total_user = %v, want 1 (scoped to their own user only)", got)
	}

	resp = doRequest(t, router, "GET", "/api/system", sudoToken, nil)
	if resp.Code != 200 {
		t.Fatalf("get system stats as sudo: %d %v", resp.Code, resp.Body)
	}
	if got := resp.Body["total_user"].(float64); got != 3 {
		t.Errorf("sudo's total_user = %v, want 3 (unscoped, whole fleet)", got)
	}
}

// TestSystemUsageHistoryReturnsRealDatesWithEveryDayPresent checks the
// zero-filling/date-range shape, not specific usage values: node_usages is
// a fleet-wide table (see GetDailyUsageHistory), shared across every test
// in this package - internal/httpapi/nodereport_test.go's own tests write
// real rows into it, so today's bucket can legitimately be nonzero
// depending on test execution order. Real value correctness is covered
// there; this test only proves every requested day gets a point (a day
// with no node_usages row must still appear, as usage:0, not be omitted).
func TestSystemUsageHistoryReturnsRealDatesWithEveryDayPresent(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "GET", "/api/system/usage-history?days=14", token, nil)
	if resp.Code != 200 {
		t.Fatalf("get usage history: %d %v", resp.Code, resp.Body)
	}
	var points []map[string]interface{}
	if err := json.Unmarshal(resp.Raw, &points); err != nil {
		t.Fatalf("decode usage history: %v", err)
	}
	if len(points) != 14 {
		t.Fatalf("got %d points, want 14", len(points))
	}
	today := time.Now().UTC().Format("2006-01-02")
	if points[13]["date"] != today {
		t.Errorf("last point's date = %v, want today (%s)", points[13]["date"], today)
	}
	// A day further back than anything any test could plausibly have
	// written must be exactly 0 - this is what actually proves zero-fill
	// works, without depending on execution order for "today".
	if usage, ok := points[0]["usage"].(float64); !ok || usage != 0 {
		t.Errorf("earliest point's usage = %v, want 0 (no test writes usage 13 days in the past)", points[0]["usage"])
	}
}

func TestUserResponseIncludesOnlineAt(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "VMess TCP", "protocol": "vmess"}})
	resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "sys_online_at_test", "proxies": map[string]interface{}{"vmess": map[string]interface{}{}},
	})
	if resp.Code != 200 {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}
	if _, ok := resp.Body["online_at"]; !ok {
		t.Error("user response is missing the online_at key entirely, want present (null is fine)")
	}
	if resp.Body["online_at"] != nil {
		t.Errorf("online_at = %v, want null for a freshly created user", resp.Body["online_at"])
	}
}
