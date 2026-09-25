package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// capacityOf reads the capacity out of a node object: (value, isNull). It fails
// the test when the key is missing altogether - the field must always be present.
func capacityOf(t *testing.T, node map[string]interface{}) (float64, bool) {
	t.Helper()
	v, present := node["capacity"]
	if !present {
		t.Fatalf("capacity key missing from node object: %v", node)
	}
	if v == nil {
		return 0, true
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("capacity = %v (%T), want a number or null", v, v)
	}
	return f, false
}

func TestCreateNodeCapacityAcceptsNullAbsentOrAValue(t *testing.T) {
	router, token := newTestRouter(t)

	cases := []struct {
		name string
		body map[string]interface{}
		want float64
		null bool
	}{
		{"cap-absent", map[string]interface{}{}, 0, true},
		{"cap-null", map[string]interface{}{"capacity": nil}, 0, true},
		{"cap-value", map[string]interface{}{"capacity": 15000}, 15000, false},
		{"cap-min", map[string]interface{}{"capacity": 1}, 1, false},
		{"cap-max", map[string]interface{}{"capacity": 10000000}, 10000000, false},
	}
	for _, c := range cases {
		body := map[string]interface{}{"name": c.name, "address": "10.0.0.7"}
		for k, v := range c.body {
			body[k] = v
		}
		resp := doRequest(t, router, "POST", "/api/node", token, body)
		if resp.Code != http.StatusOK {
			t.Fatalf("%s: create = %d %s", c.name, resp.Code, resp.Raw)
		}
		// Top level (the real panel's NodeResponse shape) and the nested copy.
		for label, obj := range map[string]map[string]interface{}{"top level": resp.Body, "node": resp.Body["node"].(map[string]interface{})} {
			if got, null := capacityOf(t, obj); got != c.want || null != c.null {
				t.Errorf("%s (%s): capacity = %v null=%v, want %v null=%v", c.name, label, got, null, c.want, c.null)
			}
		}

		id := strconv.Itoa(int(resp.Body["id"].(float64)))
		get := doRequest(t, router, "GET", "/api/node/"+id, token, nil)
		if got, null := capacityOf(t, get.Body); got != c.want || null != c.null {
			t.Errorf("%s: GET capacity = %v null=%v, want %v null=%v", c.name, got, null, c.want, c.null)
		}
	}

	// The list carries it for every node, null included.
	list := doRequest(t, router, "GET", "/api/nodes", token, nil)
	var nodes []map[string]interface{}
	if err := json.Unmarshal(list.Raw, &nodes); err != nil {
		t.Fatalf("decode list: %v: %s", err, list.Raw)
	}
	if len(nodes) != len(cases) {
		t.Fatalf("list has %d nodes, want %d", len(nodes), len(cases))
	}
	byName := map[string]map[string]interface{}{}
	for _, n := range nodes {
		byName[n["name"].(string)] = n
	}
	for _, c := range cases {
		if got, null := capacityOf(t, byName[c.name]); got != c.want || null != c.null {
			t.Errorf("%s: list capacity = %v null=%v, want %v null=%v", c.name, got, null, c.want, c.null)
		}
	}
}

func TestCreateNodeCapacityIsValidated(t *testing.T) {
	router, token := newTestRouter(t)
	for _, bad := range []interface{}{0, -5, 10000001, 99999999999, 1.5, "abc", "100", true, []int{1}} {
		resp := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{
			"name": "cap-bad", "address": "10.0.0.8", "capacity": bad,
		})
		if resp.Code != http.StatusUnprocessableEntity {
			t.Errorf("capacity %v: create = %d %s, want 422", bad, resp.Code, resp.Raw)
			continue
		}
		if detail, _ := resp.Body["detail"].(string); !strings.Contains(detail, "capacity") {
			t.Errorf("capacity %v: detail = %q, want it to name the field", bad, detail)
		}
	}
	// None of them created a node.
	list := doRequest(t, router, "GET", "/api/nodes", token, nil)
	var nodes []map[string]interface{}
	_ = json.Unmarshal(list.Raw, &nodes)
	if len(nodes) != 0 {
		t.Errorf("a rejected create left %d nodes behind", len(nodes))
	}
}

func TestUpdateNodeCapacityIsPartialAndNullClearsIt(t *testing.T) {
	router, token := newTestRouter(t)
	nodeID, _ := createTestNode(t, router, token, "cap-update-node")
	idPath := "/api/node/" + strconv.Itoa(int(nodeID))

	put := func(body map[string]interface{}) map[string]interface{} {
		resp := doRequest(t, router, "PUT", idPath, token, body)
		if resp.Code != http.StatusOK {
			t.Fatalf("PUT %v = %d %s", body, resp.Code, resp.Raw)
		}
		return resp.Body
	}

	// Capacity alone: sets it, leaves everything else exactly as it was.
	got := put(map[string]interface{}{"capacity": 8000})
	if v, null := capacityOf(t, got); v != 8000 || null {
		t.Errorf("capacity-only PUT: capacity = %v null=%v, want 8000", v, null)
	}
	if got["name"] != "cap-update-node" || got["address"] != "10.0.0.1" || got["usage_coefficient"] != 1.0 || got["status"] != "connecting" {
		t.Errorf("a capacity-only PUT changed something else: %v", got)
	}

	// Omitting the field on any other update leaves it alone.
	got = put(map[string]interface{}{"usage_coefficient": 2.5})
	if v, null := capacityOf(t, got); v != 8000 || null {
		t.Errorf("update without capacity: capacity = %v null=%v, want it unchanged at 8000", v, null)
	}
	got = put(map[string]interface{}{"name": "cap-update-node-renamed", "port": 62060})
	if v, null := capacityOf(t, got); v != 8000 || null || got["name"] != "cap-update-node-renamed" {
		t.Errorf("rename without capacity: %v, want capacity 8000 and the new name", got)
	}

	// With other fields it goes through the full update path and is applied there.
	got = put(map[string]interface{}{"usage_coefficient": 1.5, "capacity": 9000})
	if v, null := capacityOf(t, got); v != 9000 || null || got["usage_coefficient"] != 1.5 {
		t.Errorf("full update: %v, want capacity 9000 and usage_coefficient 1.5", got)
	}

	// Explicit null clears it, by either path.
	got = put(map[string]interface{}{"capacity": nil})
	if _, null := capacityOf(t, got); !null {
		t.Errorf("capacity-only null: %v, want it cleared", got)
	}
	put(map[string]interface{}{"capacity": 7000})
	got = put(map[string]interface{}{"name": "cap-update-node-again", "capacity": nil})
	if _, null := capacityOf(t, got); !null {
		t.Errorf("null with another field: %v, want it cleared", got)
	}

	// Validation on both paths: 422 and nothing changed.
	put(map[string]interface{}{"capacity": 4200})
	for _, body := range []map[string]interface{}{
		{"capacity": 0},
		{"capacity": -1},
		{"capacity": 10000001},
		{"capacity": 2.5},
		{"capacity": "many"},
		{"capacity": 0, "usage_coefficient": 3},
		{"capacity": 10000001, "name": "x"},
	} {
		resp := doRequest(t, router, "PUT", idPath, token, body)
		if resp.Code != http.StatusUnprocessableEntity {
			t.Errorf("PUT %v = %d %s, want 422", body, resp.Code, resp.Raw)
		}
	}
	after := doRequest(t, router, "GET", idPath, token, nil)
	if v, null := capacityOf(t, after.Body); v != 4200 || null || after.Body["usage_coefficient"] != 1.5 {
		t.Errorf("rejected updates changed the node: %v", after.Body)
	}

	// An unknown node is a 404 on both paths.
	for _, body := range []map[string]interface{}{{"capacity": 100}, {"capacity": 100, "usage_coefficient": 2}} {
		if resp := doRequest(t, router, "PUT", "/api/node/99999", token, body); resp.Code != http.StatusNotFound {
			t.Errorf("PUT unknown node %v = %d, want 404", body, resp.Code)
		}
	}
}

func TestNodeCapacityEditsNeverBumpTheNodeConfigVersion(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	pool := handler.store.Pool
	nodeID, secret := createTestNode(t, router, token, "cap-version-node")
	idPath := "/api/node/" + strconv.Itoa(int(nodeID))

	// The version-cache trigger fires on writes that name the profile columns; the
	// capacity is not one of them, so a bare write of it must not reach a trigger.
	stmt := `UPDATE nodes SET capacity = 5000`
	if bumpsVersion(t, pool, stmt) {
		t.Errorf("%s bumped the node_config version", stmt)
	}
	if fired := firedTriggers(t, pool, stmt); len(fired) != 0 {
		t.Errorf("%s fired %v, want no trigger at all", stmt, fired)
	}
	if fired := firedTriggers(t, pool, `UPDATE nodes SET capacity = NULL`); len(fired) != 0 {
		t.Errorf("clearing the capacity fired %v", fired)
	}

	// And through the API: setting, changing and clearing a node's capacity leaves
	// the version - and therefore every node's cached config - untouched.
	before := readDataVersion(t, pool)
	for _, capacity := range []interface{}{12000, 3000, nil, 50} {
		if resp := doRequest(t, router, "PUT", idPath, token, map[string]interface{}{"capacity": capacity}); resp.Code != http.StatusOK {
			t.Fatalf("PUT capacity %v = %d %s", capacity, resp.Code, resp.Raw)
		}
	}
	if after := readDataVersion(t, pool); after != before {
		t.Errorf("node_config version moved %d -> %d over four capacity edits", before, after)
	}

	// The node's own poll still answers 304 to its last-seen version: nothing to re-apply.
	first, header, _ := getNodeConfigRaw(t, router, secret, "")
	if first != http.StatusOK || header.Get("ETag") == "" {
		t.Fatalf("first poll = %d etag %q", first, header.Get("ETag"))
	}
	doRequest(t, router, "PUT", idPath, token, map[string]interface{}{"capacity": 777})
	if code, _, _ := getNodeConfigRaw(t, router, secret, header.Get("ETag")); code != http.StatusNotModified {
		t.Errorf("poll after a capacity edit = %d, want 304 (config unchanged)", code)
	}

	// The column keeps its own guard: a non-positive value cannot be stored.
	if _, err := pool.Exec(context.Background(), `UPDATE nodes SET capacity = 0`); err == nil {
		t.Error("capacity = 0 was accepted by the database")
	}
}

func TestNodeReportStoresTheClientConnectionCount(t *testing.T) {
	router, token := newTestRouter(t)
	pool := testPool(t)
	nodeID, secret := createTestNode(t, router, token, "client-conns-node")

	report := func(host map[string]interface{}) {
		t.Helper()
		body := nodeReportPayload(nil)
		body["host"] = host
		if resp := doRequest(t, router, "POST", "/api/internal/node-report", secret, body); resp.Code != http.StatusOK {
			t.Fatalf("node report: %d %s", resp.Code, resp.Raw)
		}
	}
	stored := func() (int32, bool) {
		t.Helper()
		var n *int32
		if err := pool.QueryRow(context.Background(),
			`SELECT client_conns FROM host_metrics WHERE node_id = $1 ORDER BY id DESC LIMIT 1`, nodeID).Scan(&n); err != nil {
			t.Fatalf("read client_conns: %v", err)
		}
		if n == nil {
			return 0, false
		}
		return *n, true
	}
	monitored := func() interface{} {
		t.Helper()
		resp := doRequest(t, router, "GET", "/api/monitoring", token, nil)
		for _, h := range resp.Body["hosts"].([]interface{}) {
			host := h.(map[string]interface{})
			if id, ok := host["node_id"].(float64); ok && int32(id) == nodeID {
				v, present := host["client_conns"]
				if !present {
					t.Fatalf("client_conns key missing from the monitoring host: %v", host)
				}
				return v
			}
		}
		t.Fatal("node missing from /api/monitoring")
		return nil
	}
	sample := func(extra map[string]interface{}) map[string]interface{} {
		host := nodeReportPayload(nil)["host"].(map[string]interface{})
		host["collected_at"] = time.Now().UTC().Format(time.RFC3339)
		for k, v := range extra {
			host[k] = v
		}
		return host
	}

	// A node that predates the field sends nothing: unknown, so NULL - not a made-up 0.
	report(sample(nil))
	if _, ok := stored(); ok {
		t.Error("an older node's report stored a client_conns value, want NULL")
	}
	if v := monitored(); v != nil {
		t.Errorf("monitoring client_conns = %v, want null for a node that does not report it", v)
	}

	// A node that reports it: stored as sent and shown next to the socket count.
	report(sample(map[string]interface{}{"client_connections": 4300}))
	if n, ok := stored(); !ok || n != 4300 {
		t.Errorf("stored client_conns = %d (present=%v), want 4300", n, ok)
	}
	if v := monitored(); v != float64(4300) {
		t.Errorf("monitoring client_conns = %v, want 4300", v)
	}

	// An honest zero is a reading, not "unknown".
	report(sample(map[string]interface{}{"client_connections": 0}))
	if n, ok := stored(); !ok || n != 0 {
		t.Errorf("stored client_conns = %d (present=%v), want a real 0", n, ok)
	}
	if v := monitored(); v != float64(0) {
		t.Errorf("monitoring client_conns = %v, want 0", v)
	}

	// The panel's own row and a node that never reported are null too.
	resp := doRequest(t, router, "GET", "/api/monitoring", token, nil)
	panel := resp.Body["hosts"].([]interface{})[0].(map[string]interface{})
	if v, present := panel["client_conns"]; !present || v != nil {
		t.Errorf("panel row client_conns = %v (present=%v), want an explicit null", v, present)
	}
}
