package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestUnknownRouteAnswersJSON is the fix for what looked like a total
// outage from a reseller bot's side: Gin's default plain-text "404 page
// not found" body made every bot that json_decode()s a response conclude
// the whole panel was unreachable, rather than "that one call 404'd".
// FastAPI - which the whole Marzban client ecosystem was written against -
// always answers JSON.
func TestUnknownRouteAnswersJSON(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "GET", "/api/definitely-not-a-real-route", token, nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.Code)
	}
	if resp.Body["detail"] != "Not Found" {
		t.Errorf("body = %v, want a JSON {\"detail\":\"Not Found\"}", resp.Body)
	}
}

// TestWrongMethodAnswersJSON405 covers the other half: the real panel
// distinguishes "no such path" from "wrong verb on a real path", and a
// client that retries on one but not the other needs that distinction.
func TestWrongMethodAnswersJSON405(t *testing.T) {
	router, token := newTestRouter(t)

	// /api/system is GET-only and has no same-level param sibling that
	// could swallow another verb (unlike, say, PUT /api/admin/token, which
	// legitimately matches /api/admin/:username on both panels).
	resp := doRequest(t, router, "DELETE", "/api/system", token, nil)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.Code)
	}
	if resp.Body["detail"] != "Method Not Allowed" {
		t.Errorf("body = %v, want a JSON {\"detail\":\"Method Not Allowed\"}", resp.Body)
	}
}

// TestCoreStatsIsReachableByANonSudoAdmin pins the one auth level that
// differs from every other /core route: the real panel gates GET /api/core
// with Admin.get_current, not check_sudo_admin. Reseller bots run as
// ordinary non-sudo admins and probe this to decide whether the panel is
// reachable at all, so a 403 here reads to them as the whole server being
// down - which is exactly how it was first reported.
func TestCoreStatsIsReachableByANonSudoAdmin(t *testing.T) {
	router, sudoToken := newTestRouter(t)
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{
		"username": "core-probe-reseller", "password": "pw12345", "is_sudo": false,
	})
	resellerToken := loginAs(t, router, "core-probe-reseller", "pw12345")

	resp := doRequest(t, router, "GET", "/api/core", resellerToken, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/core as a non-sudo admin = %d %v, want 200", resp.Code, resp.Body)
	}
	for _, key := range []string{"version", "started", "logs_websocket"} {
		if _, ok := resp.Body[key]; !ok {
			t.Errorf("CoreStats is missing %q: %v", key, resp.Body)
		}
	}

	// The rest of the /core family stays sudo-only, same as the real panel.
	cfg := doRequest(t, router, "GET", "/api/core/config", resellerToken, nil)
	if cfg.Code != http.StatusForbidden {
		t.Errorf("GET /api/core/config as non-sudo = %d, want 403", cfg.Code)
	}
}

// TestInboundsAreProxyInboundObjects pins the shape every third-party
// client indexes: the real panel's GET /api/inbounds is
// Dict[protocol, List[ProxyInbound]], and a ProxyInbound always carries
// tag/protocol/network/tls/port. This used to be a list of bare tag
// strings here, which is unindexable - a client reading entry["tag"] found
// nothing and concluded the panel had no inbounds at all.
func TestInboundsAreProxyInboundObjects(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "Shape VLESS", "protocol": "vless", "network": "tcp", "security": "tls",
			"tls_certificate": testCertPEM, "tls_key": testKeyPEM, "tls_server_name": "example.test"},
	})
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"Shape VLESS": []map[string]interface{}{{"remark": "h", "address": "1.2.3.4", "port": 8443}},
	})

	resp := doRequest(t, router, "GET", "/api/inbounds", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get inbounds: %d %v", resp.Code, resp.Body)
	}
	entries, ok := resp.Body["vless"].([]interface{})
	if !ok || len(entries) == 0 {
		t.Fatalf("vless entries = %v, want a non-empty list", resp.Body["vless"])
	}
	var found map[string]interface{}
	for _, e := range entries {
		obj, ok := e.(map[string]interface{})
		if !ok {
			t.Fatalf("entry is %T, want an object - a bare string is what broke real clients", e)
		}
		if obj["tag"] == "Shape VLESS" {
			found = obj
		}
	}
	if found == nil {
		t.Fatalf("Shape VLESS not found: %v", entries)
	}
	if found["protocol"] != "vless" || found["network"] != "tcp" || found["tls"] != "tls" {
		t.Errorf("entry = %v, want protocol=vless network=tcp tls=tls", found)
	}
	if port, ok := found["port"].(float64); !ok || int(port) != 8443 {
		t.Errorf("port = %v, want 8443 (from the inbound's primary host)", found["port"])
	}
}

// TestSystemStatsCarriesEveryRequiredField pins the host-resource half of
// SystemStats. Every field on that model is required, so a client built
// from it (SystemStats(**r.json()), or any generated client) rejects the
// whole response when one is missing - and the CPU/RAM/throughput widgets
// on every bot dashboard read exactly these.
func TestSystemStatsCarriesEveryRequiredField(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "GET", "/api/system", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get system: %d %v", resp.Code, resp.Body)
	}
	for _, key := range []string{
		"version", "mem_total", "mem_used", "cpu_cores", "cpu_usage",
		"total_user", "online_users", "users_active", "users_on_hold",
		"users_disabled", "users_expired", "users_limited",
		"incoming_bandwidth", "outgoing_bandwidth",
		"incoming_bandwidth_speed", "outgoing_bandwidth_speed",
	} {
		if _, ok := resp.Body[key]; !ok {
			t.Errorf("SystemStats is missing required field %q", key)
		}
	}
}

// TestCreateNodeDefaultsPortsAndReturnsItFlat covers two separate
// incompatibilities on the same call: the real panel defaults
// port/api_port (a client may post only name+address) and answers with the
// node's own fields at the top level, so resp["id"] is how every client
// learns the new node's id.
func TestCreateNodeDefaultsPortsAndReturnsItFlat(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{
		"name": "minimal-node", "address": "10.9.9.9",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("create node with the minimal body: %d %v", resp.Code, resp.Body)
	}
	if id, ok := resp.Body["id"].(float64); !ok || id == 0 {
		t.Errorf("id = %v, want the new node's id at the top level", resp.Body["id"])
	}
	if port, _ := resp.Body["port"].(float64); int(port) != defaultNodePort {
		t.Errorf("port = %v, want the default %d", resp.Body["port"], defaultNodePort)
	}
	if apiPort, _ := resp.Body["api_port"].(float64); int(apiPort) != defaultNodeAPIPort {
		t.Errorf("api_port = %v, want the default %d", resp.Body["api_port"], defaultNodeAPIPort)
	}
	for _, key := range []string{"xray_version", "message", "status", "usage_coefficient"} {
		if _, ok := resp.Body[key]; !ok {
			t.Errorf("NodeResponse is missing %q", key)
		}
	}
	// The dashboard's own one-time reveal panel still needs its bundle.
	if _, ok := resp.Body["setup_blob"]; !ok {
		t.Error("setup_blob disappeared from the create-node response")
	}
}

// TestUpdateNodeIsPartialAndAcceptsStatus pins the documented way to
// disable a node - PUT {"status":"disabled"} - and that a partial body
// leaves everything else alone instead of 422ing.
func TestUpdateNodeIsPartialAndAcceptsStatus(t *testing.T) {
	router, token := newTestRouter(t)
	nodeID, _ := createTestNode(t, router, token, "partial-update-node")
	idPath := "/api/node/" + strconv.Itoa(int(nodeID))

	coeff := doRequest(t, router, "PUT", idPath, token, map[string]interface{}{"usage_coefficient": 2.5})
	if coeff.Code != http.StatusOK {
		t.Fatalf("partial update = %d %v, want 200", coeff.Code, coeff.Body)
	}
	if coeff.Body["name"] != "partial-update-node" {
		t.Errorf("name = %v, want it untouched by a partial update", coeff.Body["name"])
	}
	if coeff.Body["usage_coefficient"] != 2.5 {
		t.Errorf("usage_coefficient = %v, want 2.5", coeff.Body["usage_coefficient"])
	}

	disabled := doRequest(t, router, "PUT", idPath, token, map[string]interface{}{"status": "disabled"})
	if disabled.Code != http.StatusOK {
		t.Fatalf("status=disabled update: %d %v", disabled.Code, disabled.Body)
	}
	if disabled.Body["status"] != "disabled" {
		t.Errorf("status = %v, want disabled - PUT {\"status\":\"disabled\"} is the documented way to switch a node off", disabled.Body["status"])
	}

	back := doRequest(t, router, "PUT", idPath, token, map[string]interface{}{"status": "connecting"})
	if back.Body["status"] == "disabled" {
		t.Errorf("status = %v, want the node re-enabled", back.Body["status"])
	}
}

func TestGetUserUsageReturnsMasterAndEveryNode(t *testing.T) {
	router, token := newTestRouter(t)
	createTestNode(t, router, token, "usage-node-1")

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "Usage VLESS", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "usage_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	resp := doRequest(t, router, "GET", "/api/user/usage_user/usage", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get user usage: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["username"] != "usage_user" {
		t.Errorf("username = %v, want usage_user", resp.Body["username"])
	}
	usages, ok := resp.Body["usages"].([]interface{})
	if !ok || len(usages) < 2 {
		t.Fatalf("usages = %v, want at least a Master row plus the one node", resp.Body["usages"])
	}
	master := usages[0].(map[string]interface{})
	if master["node_name"] != "Master" || master["node_id"] != nil {
		t.Errorf("first entry = %v, want the Master row with a null node_id", master)
	}
	// Every node must appear even with no traffic recorded, so a client's
	// per-node series stays stable instead of nodes popping in and out.
	found := false
	for _, u := range usages[1:] {
		if u.(map[string]interface{})["node_name"] == "usage-node-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("usages = %v, want the real node listed with a zero total", usages)
	}
}

func TestGetUsersUsageIsScopedAndShaped(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "GET", "/api/users/usage", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get users usage: %d %v", resp.Code, resp.Body)
	}
	usages, ok := resp.Body["usages"].([]interface{})
	if !ok || len(usages) == 0 {
		t.Fatalf("usages = %v, want at least the Master row", resp.Body["usages"])
	}
	if usages[0].(map[string]interface{})["node_name"] != "Master" {
		t.Errorf("first entry = %v, want the Master row", usages[0])
	}
}

func TestUsageRejectsInvertedDateRange(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "GET", "/api/users/usage?start=2026-02-01T00:00:00&end=2026-01-01T00:00:00", token, nil)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an inverted range", resp.Code)
	}
}

func TestGetAdminUsageReturnsBareInteger(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/admin", token, map[string]interface{}{
		"username": "usage_admin", "password": "pw-usage-admin",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/usage_admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	// response_model=int on the real panel - a bare number, not an object.
	if strings.TrimSpace(rec.Body.String()) != "0" {
		t.Errorf("body = %q, want the bare integer 0", rec.Body.String())
	}
}

func TestExpiredUsersListAndDelete(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "Exp VLESS", "protocol": "vless"}})

	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "expired_one",
		"proxies":  map[string]interface{}{"vless": map[string]interface{}{}},
	})
	// Moving the expiry into the past is what actually flips the status -
	// creation keeps whatever status was asked for (active by default) on
	// both panels; it's the modify path (and the review job) that
	// recomputes it.
	expired := doRequest(t, router, "PUT", "/api/user/expired_one", token, map[string]interface{}{
		"expire": 1600000000,
	})
	if expired.Body["status"] != "expired" {
		t.Fatalf("status = %v after moving expire into the past, want expired", expired.Body["status"])
	}

	listed := doRequest(t, router, "GET", "/api/users/expired", token, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list expired: %d %v", listed.Code, listed.Body)
	}
	var names []string
	if err := json.Unmarshal(listed.Raw, &names); err != nil {
		t.Fatalf("expired list is not a bare JSON array: %v (%s)", err, listed.Raw)
	}
	if len(names) != 1 || names[0] != "expired_one" {
		t.Fatalf("expired = %v, want [expired_one]", names)
	}

	deleted := doRequest(t, router, "DELETE", "/api/users/expired", token, nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete expired: %d %v", deleted.Code, deleted.Body)
	}

	// A second delete has nothing left to remove - the real panel answers
	// 404 rather than an empty list, so a caller can tell "nothing matched"
	// from "removed nothing because it all worked".
	again := doRequest(t, router, "DELETE", "/api/users/expired", token, nil)
	if again.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", again.Code)
	}
}

func TestSetUserOwnerMovesTheUser(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "Own VLESS", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/admin", token, map[string]interface{}{
		"username": "new_owner", "password": "pw-new-owner",
	})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "owned_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	resp := doRequest(t, router, "PUT", "/api/user/owned_user/set-owner?admin_username=new_owner", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("set owner: %d %v", resp.Code, resp.Body)
	}
	admin, ok := resp.Body["admin"].(map[string]interface{})
	if !ok || admin["username"] != "new_owner" {
		t.Errorf("admin = %v, want the user moved to new_owner", resp.Body["admin"])
	}

	missing := doRequest(t, router, "PUT", "/api/user/owned_user/set-owner?admin_username=nobody", token, nil)
	if missing.Code != http.StatusNotFound {
		t.Errorf("unknown admin = %d, want 404", missing.Code)
	}
}

func TestActivateNextPlanRequiresOne(t *testing.T) {
	router, token := newTestRouter(t)
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "Next VLESS", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "nextplan_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	none := doRequest(t, router, "POST", "/api/user/nextplan_user/active-next", token, nil)
	if none.Code != http.StatusNotFound {
		t.Fatalf("without a next plan = %d %v, want 404", none.Code, none.Body)
	}

	doRequest(t, router, "PUT", "/api/user/nextplan_user", token, map[string]interface{}{
		"next_plan": map[string]interface{}{"data_limit": 1073741824, "expire": 86400},
	})

	fired := doRequest(t, router, "POST", "/api/user/nextplan_user/active-next", token, nil)
	if fired.Code != http.StatusOK {
		t.Fatalf("activate next plan: %d %v", fired.Code, fired.Body)
	}
	if fired.Body["status"] != "active" {
		t.Errorf("status = %v, want active after firing the plan", fired.Body["status"])
	}
	if fired.Body["next_plan"] != nil {
		t.Errorf("next_plan = %v, want it consumed and cleared", fired.Body["next_plan"])
	}
}

func TestCoreRestartAndNodeReconnectAnswerRealShapes(t *testing.T) {
	router, token := newTestRouter(t)
	nodeID, _ := createTestNode(t, router, token, "reconnect-node")

	restart := doRequest(t, router, "POST", "/api/core/restart", token, nil)
	if restart.Code != http.StatusOK {
		t.Fatalf("core restart: %d %v", restart.Code, restart.Body)
	}

	reconnect := doRequest(t, router, "POST", "/api/node/"+strconv.Itoa(int(nodeID))+"/reconnect", token, nil)
	if reconnect.Code != http.StatusOK {
		t.Fatalf("node reconnect: %d %v", reconnect.Code, reconnect.Body)
	}
	if reconnect.Body["detail"] != "Reconnection task scheduled" {
		t.Errorf("detail = %v, want the real panel's own message", reconnect.Body["detail"])
	}

	missing := doRequest(t, router, "POST", "/api/node/999999/reconnect", token, nil)
	if missing.Code != http.StatusNotFound {
		t.Errorf("unknown node = %d, want 404", missing.Code)
	}
}

func TestNodeSettingsExposesTheCACertificate(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "GET", "/api/node/settings", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("node settings: %d %v", resp.Code, resp.Body)
	}
	cert, _ := resp.Body["certificate"].(string)
	if !strings.Contains(cert, "BEGIN CERTIFICATE") {
		t.Errorf("certificate = %q, want a real PEM block", cert)
	}
	if resp.Body["min_node_version"] == nil {
		t.Error("min_node_version is missing - the real NodeSettings model always carries it")
	}
}

func TestValidateRawXrayConfigReportsBothVerdicts(t *testing.T) {
	router, token := newTestRouter(t)

	good := doRequest(t, router, "POST", "/api/core/config/validate", token, map[string]interface{}{
		"inbounds": []map[string]interface{}{{
			"tag": "v", "port": 443, "protocol": "vless",
			"settings":       map[string]interface{}{"clients": []interface{}{}, "decryption": "none"},
			"streamSettings": map[string]interface{}{"network": "tcp", "security": "none"},
		}},
		"outbounds": []map[string]interface{}{{"tag": "direct", "protocol": "freedom"}},
	})
	if good.Code != http.StatusOK || good.Body["valid"] != true {
		t.Fatalf("valid config = %d %v, want 200 valid:true", good.Code, good.Body)
	}
	if _, ok := good.Body["checked_with_xray"]; !ok {
		t.Error("checked_with_xray is missing - the real CoreConfigValidation model always carries it")
	}

	bad := doRequest(t, router, "POST", "/api/core/config/validate", token, map[string]interface{}{"nothing": "useful"})
	if bad.Code != http.StatusOK || bad.Body["valid"] != false {
		t.Errorf("unusable config = %d %v, want 200 valid:false", bad.Code, bad.Body)
	}
}

func TestCoreConfigBackupsAnswerAnEmptyList(t *testing.T) {
	router, token := newTestRouter(t)

	resp := doRequest(t, router, "GET", "/api/core/config/backups", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("list backups: %d %v", resp.Code, resp.Body)
	}
	var backups []interface{}
	if err := json.Unmarshal(resp.Raw, &backups); err != nil {
		t.Fatalf("backups is not a bare JSON array: %v (%s)", err, resp.Raw)
	}
	if len(backups) != 0 {
		t.Errorf("backups = %v, want an empty list (this panel stores none)", backups)
	}
}
