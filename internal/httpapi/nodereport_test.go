package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// createTestNode creates a node via the real admin API and returns its id
// and the report_secret POST /api/node returns exactly once - the same
// secret a real node agent would be configured with (NODE_REPORT_SECRET).
func createTestNode(t *testing.T, router http.Handler, token, name string) (int32, string) {
	t.Helper()
	resp := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{
		"name": name, "address": "10.0.0.1", "port": 443, "api_port": 62051,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("create node: %d %v", resp.Code, resp.Body)
	}
	node := resp.Body["node"].(map[string]interface{})
	id := int32(node["id"].(float64))
	secret := resp.Body["report_secret"].(string)
	if secret == "" {
		t.Fatal("report_secret missing from create-node response")
	}
	return id, secret
}

func nodeReportPayload(users []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"users": users,
		"host": map[string]interface{}{
			"collected_at":            time.Now().UTC().Format(time.RFC3339),
			"cpu_total_jiffies":       2000,
			"cpu_idle_jiffies":        1000,
			"cpu_cores":               2,
			"mem_total_bytes":         1000,
			"mem_available_bytes":     400,
			"disk_total_bytes":        2000,
			"disk_used_bytes":         500,
			"rx_bytes":                1000,
			"tx_bytes":                2000,
			"connections_established": 3,
			"xray_running":            true,
		},
	}
}

func TestNodeReportRejectsMissingOrInvalidSecret(t *testing.T) {
	router, _ := newTestRouter(t)

	resp := doRequest(t, router, "POST", "/api/internal/node-report", "", nodeReportPayload(nil))
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("missing secret: %d, want 401", resp.Code)
	}

	resp = doRequest(t, router, "POST", "/api/internal/node-report", "not-a-real-secret", nodeReportPayload(nil))
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("invalid secret: %d, want 401", resp.Code)
	}
}

func TestNodeReportUpdatesUserUsageAndOnlineAt(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "report-test-node-1")

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	createResp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "node_report_user_a", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	if createResp.Code != http.StatusOK {
		t.Fatalf("create user: %d %v", createResp.Code, createResp.Body)
	}

	before := time.Now().UTC()
	reportResp := doRequest(t, router, "POST", "/api/internal/node-report", secret, nodeReportPayload([]map[string]interface{}{
		{"username": "node_report_user_a", "uplink": 1000, "downlink": 2000},
	}))
	if reportResp.Code != http.StatusOK {
		t.Fatalf("node report: %d %v", reportResp.Code, reportResp.Body)
	}

	getResp := doRequest(t, router, "GET", "/api/user/node_report_user_a", token, nil)
	if used := int64(getResp.Body["used_traffic"].(float64)); used != 3000 {
		t.Errorf("used_traffic = %d, want 3000", used)
	}
	onlineAtRaw, _ := getResp.Body["online_at"].(string)
	if onlineAtRaw == "" {
		t.Fatal("online_at not set after a report carrying traffic for this user")
	}
	onlineAt, err := time.Parse(time.RFC3339, onlineAtRaw)
	if err != nil {
		t.Fatalf("parse online_at: %v", err)
	}
	if onlineAt.Before(before) {
		t.Errorf("online_at = %v, want >= %v", onlineAt, before)
	}
}

func TestNodeReportUpsertsHourlyBucketAcrossTwoReports(t *testing.T) {
	router, token := newTestRouter(t)
	pool := testPool(t)
	nodeID, secret := createTestNode(t, router, token, "report-test-node-2")

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "node_report_user_b", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	for i := 0; i < 2; i++ {
		resp := doRequest(t, router, "POST", "/api/internal/node-report", secret, nodeReportPayload([]map[string]interface{}{
			{"username": "node_report_user_b", "uplink": 100, "downlink": 200},
		}))
		if resp.Code != http.StatusOK {
			t.Fatalf("report #%d: %d %v", i, resp.Code, resp.Body)
		}
	}

	var rowCount int
	var totalUsed int64
	err := pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(sum(used_traffic), 0) FROM node_user_usages
		 WHERE node_id = $1 AND user_id = (SELECT id FROM users WHERE username = 'node_report_user_b')`,
		nodeID,
	).Scan(&rowCount, &totalUsed)
	if err != nil {
		t.Fatalf("query node_user_usages: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("row count = %d, want 1 (two reports in the same hour must upsert, not duplicate)", rowCount)
	}
	if totalUsed != 600 {
		t.Errorf("summed used_traffic = %d, want 600 (2 reports x 300 each)", totalUsed)
	}

	var nodeUsageRowCount int
	var nodeUplink, nodeDownlink int64
	err = pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(sum(uplink),0), COALESCE(sum(downlink),0) FROM node_usages WHERE node_id = $1`,
		nodeID,
	).Scan(&nodeUsageRowCount, &nodeUplink, &nodeDownlink)
	if err != nil {
		t.Fatalf("query node_usages: %v", err)
	}
	if nodeUsageRowCount != 1 || nodeUplink != 200 || nodeDownlink != 400 {
		t.Errorf("node_usages = rows=%d uplink=%d downlink=%d, want rows=1 uplink=200 downlink=400",
			nodeUsageRowCount, nodeUplink, nodeDownlink)
	}
}

func TestNodeReportAggregatesAdminUsage(t *testing.T) {
	router, token := newTestRouter(t)
	pool := testPool(t)
	_, secret := createTestNode(t, router, token, "report-test-node-3")

	adminResp := doRequest(t, router, "POST", "/api/admin", token, map[string]interface{}{
		"username": "report_test_reseller", "password": "SomePassword123", "is_sudo": false,
	})
	if adminResp.Code != http.StatusOK {
		t.Fatalf("create admin: %d %v", adminResp.Code, adminResp.Body)
	}
	adminToken := loginAs(t, router, "report_test_reseller", "SomePassword123")

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", adminToken, map[string]interface{}{
		"username": "node_report_reseller_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	doRequest(t, router, "POST", "/api/internal/node-report", secret, nodeReportPayload([]map[string]interface{}{
		{"username": "node_report_reseller_user", "uplink": 400, "downlink": 100},
	}))

	var usersUsage int64
	err := pool.QueryRow(context.Background(),
		"SELECT users_usage FROM admins WHERE username = 'report_test_reseller'",
	).Scan(&usersUsage)
	if err != nil {
		t.Fatalf("query admins.users_usage: %v", err)
	}
	if usersUsage != 500 {
		t.Errorf("admins.users_usage = %d, want 500", usersUsage)
	}
}

func TestGetMonitoringReflectsPushedHostMetricsAndStaleness(t *testing.T) {
	router, token := newTestRouter(t)
	pool := testPool(t)
	nodeID, secret := createTestNode(t, router, token, "monitoring-test-node")

	reportResp := doRequest(t, router, "POST", "/api/internal/node-report", secret, nodeReportPayload(nil))
	if reportResp.Code != http.StatusOK {
		t.Fatalf("node report: %d %v", reportResp.Code, reportResp.Body)
	}

	getResp := doRequest(t, router, "GET", "/api/monitoring", token, nil)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get monitoring: %d %v", getResp.Code, getResp.Body)
	}
	hosts := getResp.Body["hosts"].([]interface{})
	var found map[string]interface{}
	for _, h := range hosts {
		host := h.(map[string]interface{})
		if id, ok := host["node_id"].(float64); ok && int32(id) == nodeID {
			found = host
		}
	}
	if found == nil {
		t.Fatalf("node %d not present in /api/monitoring response: %+v", nodeID, hosts)
	}
	if found["reachable"] != true {
		t.Errorf("reachable = %v, want true right after a fresh report", found["reachable"])
	}
	if found["stale"] != false {
		t.Errorf("stale = %v, want false right after a fresh report", found["stale"])
	}

	// Push the stored sample's collected_at into the past to simulate a
	// node that stopped reporting, and confirm the read path (not any
	// background poller - there is none in the push model) computes
	// staleness purely from that timestamp.
	_, err := pool.Exec(context.Background(),
		"UPDATE host_metrics SET collected_at = now() - interval '10 minutes' WHERE node_id = $1", nodeID,
	)
	if err != nil {
		t.Fatalf("backdate collected_at: %v", err)
	}
	getResp = doRequest(t, router, "GET", "/api/monitoring", token, nil)
	hosts = getResp.Body["hosts"].([]interface{})
	for _, h := range hosts {
		host := h.(map[string]interface{})
		if id, ok := host["node_id"].(float64); ok && int32(id) == nodeID {
			found = host
		}
	}
	if found["stale"] != true {
		t.Errorf("stale = %v, want true 10 minutes after the last report", found["stale"])
	}
}

func TestGetNodesUsageReflectsPushedTraffic(t *testing.T) {
	router, token := newTestRouter(t)
	_, secret := createTestNode(t, router, token, "usage-test-node")

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "node_usage_test_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})
	doRequest(t, router, "POST", "/api/internal/node-report", secret, nodeReportPayload([]map[string]interface{}{
		{"username": "node_usage_test_user", "uplink": 7000, "downlink": 3000},
	}))

	resp := doRequest(t, router, "GET", "/api/nodes/usage", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get nodes usage: %d %v", resp.Code, resp.Body)
	}
	usages := resp.Body["usages"].([]interface{})
	var total int64
	for _, u := range usages {
		row := u.(map[string]interface{})
		total += int64(row["uplink"].(float64)) + int64(row["downlink"].(float64))
	}
	if total < 10000 {
		t.Errorf("total uplink+downlink across all nodes = %d, want >= 10000", total)
	}
}
