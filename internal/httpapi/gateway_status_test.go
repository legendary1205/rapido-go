package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func hostReportPayloadWithConnections(connections int) map[string]interface{} {
	return map[string]interface{}{
		"users": nil,
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
			"connections_established": connections,
			"xray_running":            true,
		},
	}
}

// TestGatewayStatusReflectsRealConnectionsAndExcludesStaleNodes is the
// real proof for sub-phase 3: crowdedness isn't a placeholder zero, it's
// a live sum of exactly what real nodes just reported, and a node that
// stopped reporting drops out instead of dragging its last-known count
// along forever.
func TestGatewayStatusReflectsRealConnectionsAndExcludesStaleNodes(t *testing.T) {
	router, token := newTestRouter(t)
	pool := testPool(t)

	nodeAID, secretA := createTestNode(t, router, token, "crowdedness-node-a")
	_, secretB := createTestNode(t, router, token, "crowdedness-node-b")

	reportA := doRequest(t, router, "POST", "/api/internal/node-report", secretA, hostReportPayloadWithConnections(3))
	if reportA.Code != http.StatusOK {
		t.Fatalf("report A: %d %v", reportA.Code, reportA.Body)
	}
	reportB := doRequest(t, router, "POST", "/api/internal/node-report", secretB, hostReportPayloadWithConnections(5))
	if reportB.Code != http.StatusOK {
		t.Fatalf("report B: %d %v", reportB.Code, reportB.Body)
	}

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]string{{"tag": "Crowdedness VLESS", "protocol": "vless"}})

	secretResp := doRequest(t, router, "GET", "/api/settings/gateway", token, nil)
	gatewaySecret, _ := secretResp.Body["secret"].(string)

	status := doRequest(t, router, "GET", "/api/internal/gateway/status", gatewaySecret, nil)
	if status.Code != http.StatusOK {
		t.Fatalf("gateway status: %d %v", status.Code, status.Body)
	}
	crowdedness, _ := status.Body["crowdedness"].(float64)
	if int(crowdedness) != 8 {
		t.Errorf("crowdedness = %v, want 3+5=8", crowdedness)
	}
	hosts, _ := status.Body["hosts"].([]interface{})
	foundOurTag := false
	for _, hRaw := range hosts {
		hMap := hRaw.(map[string]interface{})
		if hMap["tag"] == "Crowdedness VLESS" {
			foundOurTag = true
			if hMap["protocol"] != "vless" {
				t.Errorf("host protocol = %v, want vless", hMap["protocol"])
			}
		}
	}
	if !foundOurTag {
		t.Errorf("expected the auto-created default host for 'Crowdedness VLESS' in the status response, got %v", hosts)
	}

	// Backdate node A's report to simulate it going offline - it must drop
	// out of the sum entirely (see computePanelCrowdedness's own doc
	// comment on why: a stale last-known count would make a dead node
	// look like it's still carrying live traffic).
	if _, err := pool.Exec(context.Background(),
		"UPDATE host_metrics SET collected_at = now() - interval '10 minutes' WHERE node_id = $1", nodeAID,
	); err != nil {
		t.Fatalf("backdate node A: %v", err)
	}
	statusAfter := doRequest(t, router, "GET", "/api/internal/gateway/status", gatewaySecret, nil)
	crowdednessAfter, _ := statusAfter.Body["crowdedness"].(float64)
	if int(crowdednessAfter) != 5 {
		t.Errorf("crowdedness after node A went stale = %v, want just B's 5", crowdednessAfter)
	}
}
