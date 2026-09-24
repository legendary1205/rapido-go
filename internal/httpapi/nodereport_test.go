package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/resellerapi"
	"github.com/legendary1205/rapido-go/internal/resellerusagejob"
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

// TestNodeReportBatchHandlesMultipleUsersAcrossMultipleAdminsInOneReport is
// the real regression test for the bulk-query rewrite of handleNodeReport
// (BulkIncrementUserUsage/BulkUpsertNodeUserUsage/BulkIncrementAdminUsage,
// replacing a per-user loop that cost ~430 sequential round trips for a
// 200-user report - confirmed via a 30k-user/10-node load test at ~7s per
// call). The old per-row loop handled multiple users trivially by
// construction; the new array-based queries need a real multi-row,
// multi-admin case to prove the parallel id/delta arrays stay aligned and
// that a zero-delta user (skipped before the arrays are even built) doesn't
// shift anything after it.
func TestNodeReportBatchHandlesMultipleUsersAcrossMultipleAdminsInOneReport(t *testing.T) {
	router, token := newTestRouter(t)
	pool := testPool(t)
	_, secret := createTestNode(t, router, token, "report-test-node-batch")

	adminAResp := doRequest(t, router, "POST", "/api/admin", token, map[string]interface{}{
		"username": "batch_admin_a", "password": "SomePassword123", "is_sudo": false,
	})
	adminBResp := doRequest(t, router, "POST", "/api/admin", token, map[string]interface{}{
		"username": "batch_admin_b", "password": "SomePassword123", "is_sudo": false,
	})
	if adminAResp.Code != http.StatusOK || adminBResp.Code != http.StatusOK {
		t.Fatalf("create admins: %d %v / %d %v", adminAResp.Code, adminAResp.Body, adminBResp.Code, adminBResp.Body)
	}
	tokenA := loginAs(t, router, "batch_admin_a", "SomePassword123")
	tokenB := loginAs(t, router, "batch_admin_b", "SomePassword123")

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	for _, c := range []struct {
		username string
		adminTok string
	}{
		{"batch_user_1", tokenA},
		{"batch_user_2", tokenA},
		{"batch_user_3", tokenB},
		{"batch_user_zero", tokenB}, // gets no traffic in the report below
	} {
		resp := doRequest(t, router, "POST", "/api/user", c.adminTok, map[string]interface{}{
			"username": c.username, "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create %s: %d %v", c.username, resp.Code, resp.Body)
		}
	}

	reportResp := doRequest(t, router, "POST", "/api/internal/node-report", secret, nodeReportPayload([]map[string]interface{}{
		{"username": "batch_user_1", "uplink": 1000, "downlink": 0},
		{"username": "batch_user_2", "uplink": 0, "downlink": 2000},
		{"username": "batch_user_3", "uplink": 500, "downlink": 500},
		{"username": "batch_user_zero", "uplink": 0, "downlink": 0},
	}))
	if reportResp.Code != http.StatusOK {
		t.Fatalf("batch node report: %d %v", reportResp.Code, reportResp.Body)
	}

	for _, c := range []struct {
		username string
		want     int64
	}{
		{"batch_user_1", 1000},
		{"batch_user_2", 2000},
		{"batch_user_3", 1000},
		{"batch_user_zero", 0},
	} {
		getResp := doRequest(t, router, "GET", "/api/user/"+c.username, token, nil)
		if used := int64(getResp.Body["used_traffic"].(float64)); used != c.want {
			t.Errorf("%s used_traffic = %d, want %d", c.username, used, c.want)
		}
	}

	var usageA, usageB int64
	if err := pool.QueryRow(context.Background(), "SELECT users_usage FROM admins WHERE username = 'batch_admin_a'").Scan(&usageA); err != nil {
		t.Fatalf("query batch_admin_a usage: %v", err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT users_usage FROM admins WHERE username = 'batch_admin_b'").Scan(&usageB); err != nil {
		t.Fatalf("query batch_admin_b usage: %v", err)
	}
	if usageA != 3000 {
		t.Errorf("batch_admin_a users_usage = %d, want 3000 (batch_user_1 + batch_user_2)", usageA)
	}
	if usageB != 1000 {
		t.Errorf("batch_admin_b users_usage = %d, want 1000 (batch_user_3 only, batch_user_zero contributes nothing)", usageB)
	}

	var nodeUserUsageRows int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM node_user_usages").Scan(&nodeUserUsageRows); err != nil {
		t.Fatalf("query node_user_usages count: %v", err)
	}
	if nodeUserUsageRows != 3 {
		t.Errorf("node_user_usages row count = %d, want 3 (one per user with nonzero delta, none for batch_user_zero)", nodeUserUsageRows)
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

// TestNodeReportQueuesResellerUsageAndTheJobBillsItExactlyOnce covers the
// reseller bot's billing end to end against a real database: traffic is
// queued only while the integration is configured, a report the bot rejects
// stays queued, and an accepted one is never sent again.
func TestNodeReportQueuesResellerUsageAndTheJobBillsItExactlyOnce(t *testing.T) {
	router, token, h := newTestRouterAndHandler(t)
	pool := testPool(t)
	_, secret := createTestNode(t, router, token, "report-test-node-billing")

	if resp := doRequest(t, router, "POST", "/api/admin", token, map[string]interface{}{
		"username": "billed_reseller", "password": "SomePassword123", "is_sudo": false,
	}); resp.Code != http.StatusOK {
		t.Fatalf("create admin: %d %v", resp.Code, resp.Body)
	}
	resellerToken := loginAs(t, router, "billed_reseller", "SomePassword123")
	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{{"tag": "VLESS TCP", "protocol": "vless"}})
	doRequest(t, router, "POST", "/api/user", resellerToken, map[string]interface{}{
		"username": "billed_user", "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
	})

	report := func(bytes int) {
		t.Helper()
		resp := doRequest(t, router, "POST", "/api/internal/node-report", secret, nodeReportPayload([]map[string]interface{}{
			{"username": "billed_user", "uplink": bytes, "downlink": 0},
		}))
		if resp.Code != http.StatusOK {
			t.Fatalf("node report: %d %v", resp.Code, resp.Body)
		}
	}
	queue := func() (pending, reported int64) {
		t.Helper()
		err := pool.QueryRow(context.Background(),
			`SELECT COALESCE(sum(pending), 0), COALESCE(sum(reported), 0) FROM reseller_api_usage_queue`).Scan(&pending, &reported)
		if err != nil {
			t.Fatalf("query reseller_api_usage_queue: %v", err)
		}
		return pending, reported
	}

	// Not configured yet: users_usage grows, nothing is queued for billing.
	report(1000)
	if pending, reported := queue(); pending != 0 || reported != 0 {
		t.Fatalf("queued before the integration was configured: pending=%d reported=%d", pending, reported)
	}

	var botDown atomic.Bool
	bot := newCaptureServer(t, func(w http.ResponseWriter, body []byte) {
		if botDown.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	if resp := doRequest(t, router, "PUT", "/api/settings/integrations", token, map[string]interface{}{
		"reseller_api_secret": "test-secret", "reseller_api_url": bot.URL,
	}); resp.Code != http.StatusOK {
		t.Fatalf("configure reseller API: %d %v", resp.Code, resp.Body)
	}

	settings := func(ctx context.Context) (integrationsettings.Values, error) {
		row, err := h.store.CachedGetIntegrationSettings(ctx)
		if err != nil {
			return integrationsettings.Values{}, err
		}
		return integrationsettings.Resolve(row, integrationsettings.Values{}), nil
	}
	reporter := resellerapi.NewClient(&http.Client{Timeout: 5 * time.Second})
	tick := func() error {
		return resellerusagejob.Tick(context.Background(), h.store.Queries, reporter, settings)
	}

	report(300)
	report(200)
	if pending, _ := queue(); pending != 500 {
		t.Fatalf("pending = %d, want 500", pending)
	}

	if err := tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}
	bodies := bot.bodies()
	if len(bodies) != 1 || bodies[0] != `[{"username":"billed_reseller","usage":500}]` {
		t.Fatalf("bot received %q, want one report of 500 bytes", bodies)
	}
	if pending, reported := queue(); pending != 0 || reported != 500 {
		t.Fatalf("after delivery: pending=%d reported=%d, want 0/500", pending, reported)
	}

	if err := tick(); err != nil {
		t.Fatalf("idle tick: %v", err)
	}
	if n := len(bot.bodies()); n != 1 {
		t.Fatalf("an accepted report was sent again (%d requests)", n)
	}

	botDown.Store(true)
	report(70)
	if err := tick(); err == nil {
		t.Fatal("tick against a failing bot returned no error")
	}
	if pending, reported := queue(); pending != 70 || reported != 500 {
		t.Fatalf("after a rejected report: pending=%d reported=%d, want 70/500", pending, reported)
	}

	botDown.Store(false)
	if err := tick(); err != nil {
		t.Fatalf("tick after the bot recovered: %v", err)
	}
	bodies = bot.bodies()
	if last := bodies[len(bodies)-1]; last != `[{"username":"billed_reseller","usage":70}]` {
		t.Fatalf("retry sent %q, want the 70 bytes that were held back", last)
	}
	if pending, reported := queue(); pending != 0 || reported != 570 {
		t.Fatalf("after the retry: pending=%d reported=%d, want 0/570", pending, reported)
	}

	var usersUsage int64
	if err := pool.QueryRow(context.Background(), "SELECT users_usage FROM admins WHERE username = 'billed_reseller'").Scan(&usersUsage); err != nil {
		t.Fatalf("query users_usage: %v", err)
	}
	if usersUsage != 1570 {
		t.Errorf("users_usage = %d, want 1570 (queueing must not change what the panel itself counts)", usersUsage)
	}
}

// TestGetMonitoringDecodesTheNodesTunnelShape stores a sample in the exact
// shape hostmetrics.Sample.Tunnels marshals to (peers carry the handshake
// age, not a flat last_handshake) and checks every documented DTO field. The
// row is inserted directly so this pins the panel's decoding on its own,
// independent of what a given node build chooses to send.
func TestGetMonitoringDecodesTheNodesTunnelShape(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	nodeID, _ := createTestNode(t, router, token, "tunnel-monitoring-node")

	collected := time.Now().UTC().Truncate(time.Second)
	payload, err := json.Marshal(map[string]interface{}{
		"collected_at": collected.Format(time.RFC3339), "uptime_seconds": 100, "load_1m": 0.5, "xray_running": true,
		"tunnels": []map[string]interface{}{
			{ // healthy: two peers, the freshest handshake wins
				"name": "uk", "up": true, "present": true, "rx_bytes": 1, "tx_bytes": 2, "probe_ms": 41.2,
				"peers": []map[string]interface{}{
					{"endpoint": "1.2.3.4:51820", "last_handshake_age_seconds": 40.0, "rx_bytes": 1, "tx_bytes": 2},
					{"endpoint": "5.6.7.8:51820", "last_handshake_age_seconds": 12.5, "rx_bytes": 0, "tx_bytes": 0},
				},
			},
			{ // exists but never handshaked, probe failing, traffic falling back to direct
				"name": "de", "up": false, "present": true, "rx_bytes": 0, "tx_bytes": 0,
				"peers": []map[string]interface{}{{"endpoint": "9.9.9.9:51820", "rx_bytes": 0, "tx_bytes": 0}},
				"error": "probe timeout", "fallback_active": true,
			},
			{"name": "fr", "up": false, "present": false, "rx_bytes": 0, "tx_bytes": 0}, // configured, interface missing
			{"name": "legacy", "up": true, "rx_bytes": 5, "tx_bytes": 6},                // stored before present existed
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := handler.store.Queries.InsertHostMetric(context.Background(), generated.InsertHostMetricParams{
		NodeID:      pgtype.Int4{Int32: nodeID, Valid: true},
		CollectedAt: pgtype.Timestamptz{Time: collected, Valid: true},
		TunnelsUp:   pgtype.Int4{Int32: 2, Valid: true}, TunnelsTotal: pgtype.Int4{Int32: 4, Valid: true},
		Healthy: true, Payload: pgtype.Text{String: string(payload), Valid: true},
	}); err != nil {
		t.Fatalf("insert host metric: %v", err)
	}

	resp := doRequest(t, router, "GET", "/api/monitoring", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get monitoring: %d %v", resp.Code, resp.Body)
	}
	var host map[string]interface{}
	for _, h := range resp.Body["hosts"].([]interface{}) {
		if m := h.(map[string]interface{}); m["node_id"] == float64(nodeID) {
			host = m
		}
	}
	if host == nil {
		t.Fatalf("node %d missing from /api/monitoring: %v", nodeID, resp.Body["hosts"])
	}
	if host["tunnels_up"] != float64(2) || host["tunnels_total"] != float64(4) || host["healthy"] != true {
		t.Errorf("tunnels_up/total/healthy = %v/%v/%v, want 2/4/true (the stored summary, untouched)", host["tunnels_up"], host["tunnels_total"], host["healthy"])
	}
	tunnels := host["tunnels"].([]interface{})
	if len(tunnels) != 4 {
		t.Fatalf("tunnels = %v, want 4", tunnels)
	}
	byName := map[string]map[string]interface{}{}
	for _, tn := range tunnels {
		m := tn.(map[string]interface{})
		byName[m["name"].(string)] = m
	}

	uk := byName["uk"]
	if uk["up"] != true || uk["present"] != true || uk["rx_bytes"] != float64(1) || uk["tx_bytes"] != float64(2) {
		t.Errorf("uk = %v, want up/present with rx 1 tx 2", uk)
	}
	if uk["handshake_age_seconds"] != 12.5 {
		t.Errorf("uk handshake_age_seconds = %v, want 12.5 (the minimum across peers)", uk["handshake_age_seconds"])
	}
	// 12.5s rounds to 13s before the collection time.
	if want := float64(collected.Unix() - 13); uk["last_handshake"] != want {
		t.Errorf("uk last_handshake = %v, want %v (collected_at minus the freshest peer's age)", uk["last_handshake"], want)
	}
	if uk["probe_ms"] != 41.2 || uk["fallback_active"] != false {
		t.Errorf("uk probe_ms/fallback_active = %v/%v, want 41.2/false", uk["probe_ms"], uk["fallback_active"])
	}
	if _, has := uk["error"]; has {
		t.Errorf("uk error = %v, want the key omitted when empty", uk["error"])
	}

	de := byName["de"]
	if de["up"] != false || de["present"] != true || de["last_handshake"] != float64(0) {
		t.Errorf("de = %v, want down, present, last_handshake 0 (never handshaked)", de)
	}
	if v, has := de["handshake_age_seconds"]; !has || v != nil {
		t.Errorf("de handshake_age_seconds = %v (present=%v), want an explicit null", v, has)
	}
	if v, has := de["probe_ms"]; !has || v != nil {
		t.Errorf("de probe_ms = %v (present=%v), want an explicit null", v, has)
	}
	if de["error"] != "probe timeout" || de["fallback_active"] != true {
		t.Errorf("de error/fallback_active = %v/%v, want \"probe timeout\"/true", de["error"], de["fallback_active"])
	}

	if fr := byName["fr"]; fr["present"] != false || fr["up"] != false {
		t.Errorf("fr = %v, want present=false up=false (configured, interface missing)", fr)
	}
	legacy := byName["legacy"]
	if legacy["present"] != true || legacy["up"] != true || legacy["last_handshake"] != float64(0) || legacy["handshake_age_seconds"] != nil {
		t.Errorf("legacy = %v, want present defaulting to true with no handshake", legacy)
	}
}
