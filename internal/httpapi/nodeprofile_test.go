package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

type nodeCfgInbound struct {
	Tag         string `json:"tag"`
	ListenPort  int    `json:"listen_port"`
	ListenPorts []int  `json:"listen_ports"`
	Users       []struct {
		Name string `json:"name"`
		UUID string `json:"uuid"`
	} `json:"users"`
}

// nodeCfgView is the part of a node-config response these tests read.
type nodeCfgView struct {
	Version  string           `json:"version"`
	Inbounds []nodeCfgInbound `json:"inbounds"`
	Core     struct {
		LogLevel     string `json:"log_level"`
		SniffEnabled bool   `json:"sniff_enabled"`
		Outbounds    []struct {
			Tag    string `json:"tag"`
			Type   string `json:"type"`
			Server string `json:"server"`
		} `json:"outbounds"`
		RoutingRules []struct {
			Inbound     []string `json:"inbound"`
			InboundPort []int    `json:"inbound_port"`
			OutboundTag string   `json:"outbound_tag"`
		} `json:"routing_rules"`
		DNSServers []struct {
			Tag string `json:"tag"`
		} `json:"dns_servers"`
	} `json:"core"`
}

func (v nodeCfgView) tags() []string {
	out := []string{}
	for _, in := range v.Inbounds {
		out = append(out, in.Tag)
	}
	return out
}

func (v nodeCfgView) inbound(t *testing.T, tag string) nodeCfgInbound {
	t.Helper()
	for _, in := range v.Inbounds {
		if in.Tag == tag {
			return in
		}
	}
	t.Fatalf("inbound %q not in %v", tag, v.tags())
	return nodeCfgInbound{}
}

func decodeNodeCfg(t *testing.T, raw []byte) nodeCfgView {
	t.Helper()
	var v nodeCfgView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode node config: %v\n%.300s", err, raw)
	}
	return v
}

// getNodeConfigRaw is GET /api/internal/node-config with the one request
// header doRequest cannot send.
func getNodeConfigRaw(t *testing.T, router http.Handler, secret, ifNoneMatch string) (int, http.Header, []byte) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/internal/node-config", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code, rec.Header(), rec.Body.Bytes()
}

func pollNodeConfig(t *testing.T, router http.Handler, secret string) nodeCfgView {
	t.Helper()
	code, _, body := getNodeConfigRaw(t, router, secret, "")
	if code != http.StatusOK {
		t.Fatalf("node-config: %d %s", code, body)
	}
	return decodeNodeCfg(t, body)
}

// seedProfileFleet is three inbounds - a single-port vless, a single-port
// trojan and a multi-port vless (20000-20002, primary 20002) - and one user
// on both protocols.
func seedProfileFleet(t *testing.T, router http.Handler, token string) {
	t.Helper()
	if resp := doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "alpha", "protocol": "vless"},
		{"tag": "beta", "protocol": "trojan"},
		{"tag": "multi", "protocol": "vless"},
	}); resp.Code != http.StatusOK {
		t.Fatalf("sync inbounds: %d %v", resp.Code, resp.Body)
	}
	if resp := doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"alpha": []map[string]interface{}{{"remark": "a", "address": "1.2.3.4", "port": 8443}},
		"beta":  []map[string]interface{}{{"remark": "b", "address": "1.2.3.4", "port": 2087}},
		"multi": multiPortHosts(),
	}); resp.Code != http.StatusOK {
		t.Fatalf("put hosts: %d %v", resp.Code, resp.Body)
	}
	if resp := doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
		"username": "pp_user_a", "proxies": map[string]interface{}{"vless": map[string]interface{}{}, "trojan": map[string]interface{}{}},
	}); resp.Code != http.StatusOK {
		t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}
}

// createProfileNode creates a node with the given extra request fields and
// returns its id and report secret.
func createProfileNode(t *testing.T, router http.Handler, token, name string, extra map[string]interface{}) (int32, string) {
	t.Helper()
	body := map[string]interface{}{"name": name, "address": "10.0.0.1", "port": 443, "api_port": 62051}
	for k, v := range extra {
		body[k] = v
	}
	resp := doRequest(t, router, "POST", "/api/node", token, body)
	if resp.Code != http.StatusOK {
		t.Fatalf("create node %s: %d %v", name, resp.Code, resp.Body)
	}
	return int32(resp.Body["id"].(float64)), resp.Body["report_secret"].(string)
}

func TestNodeProfileDefaultPayloadIsUnchanged(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	seedProfileFleet(t, router, token)
	id, secret := createProfileNode(t, router, token, "np-default", nil)

	code, header, body := getNodeConfigRaw(t, router, secret, "")
	if code != http.StatusOK {
		t.Fatalf("node-config: %d %s", code, body)
	}
	want, err := handler.buildNodeConfigBody(context.Background(), nodeProfile{})
	if err != nil {
		t.Fatalf("buildNodeConfigBody: %v", err)
	}
	if string(body) != want {
		t.Fatalf("a node with the default profile is served different bytes than the reference build\n got: %.300s\nwant: %.300s", body, want)
	}
	view := decodeNodeCfg(t, body)
	if header.Get("ETag") != `"`+view.Version+`"` {
		t.Errorf("ETag = %q, want the payload version quoted (%q)", header.Get("ETag"), view.Version)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type = %q", header.Get("Content-Type"))
	}
	if !reflect.DeepEqual(view.tags(), []string{"alpha", "beta", "multi"}) {
		t.Errorf("inbounds = %v, want all three", view.tags())
	}

	// A node nobody customised serializes with none of the profile keys.
	get := doRequest(t, router, "GET", "/api/node/"+strconv.Itoa(int(id)), token, nil)
	for _, k := range []string{"inbound_tags", "listen_ports", "core_overrides"} {
		if _, has := get.Body[k]; has {
			t.Errorf("default node carries %q: %v", k, get.Body)
		}
	}
}

func TestNodeProfileServesOnlyItsInboundsAndPorts(t *testing.T) {
	router, token := newTestRouter(t)
	seedProfileFleet(t, router, token)
	_, defaultSecret := createProfileNode(t, router, token, "np-all", nil)
	_, tagsSecret := createProfileNode(t, router, token, "np-tags", map[string]interface{}{"inbound_tags": []string{"alpha"}})
	_, portsSecret := createProfileNode(t, router, token, "np-ports", map[string]interface{}{"listen_ports": []int{8443, 20000, 20002, 9}})
	_, bothSecret := createProfileNode(t, router, token, "np-both", map[string]interface{}{
		"inbound_tags": []string{"multi", "beta"}, "listen_ports": []int{20001, 2087},
	})

	all := pollNodeConfig(t, router, defaultSecret)
	if !reflect.DeepEqual(all.tags(), []string{"alpha", "beta", "multi"}) {
		t.Errorf("default node inbounds = %v, want all three (a profile elsewhere must not leak)", all.tags())
	}
	if got := all.inbound(t, "multi").ListenPorts; !reflect.DeepEqual(got, []int{20000, 20001, 20002}) {
		t.Errorf("default node multi ports = %v", got)
	}

	tagsOnly := pollNodeConfig(t, router, tagsSecret)
	if !reflect.DeepEqual(tagsOnly.tags(), []string{"alpha"}) {
		t.Errorf("inbound_tags node serves %v, want [alpha]", tagsOnly.tags())
	}
	if users := tagsOnly.inbound(t, "alpha").Users; len(users) != 1 || users[0].Name != "pp_user_a" {
		t.Errorf("alpha users = %+v, want the user list intact", users)
	}

	ports := pollNodeConfig(t, router, portsSecret)
	if !reflect.DeepEqual(ports.tags(), []string{"alpha", "multi"}) {
		t.Fatalf("listen_ports node serves %v, want [alpha multi] (beta's port 2087 is not listed)", ports.tags())
	}
	multi := ports.inbound(t, "multi")
	if !reflect.DeepEqual(multi.ListenPorts, []int{20000, 20002}) || multi.ListenPort != 20002 {
		t.Errorf("multi = port %d ports %v, want 20002 and [20000 20002] (20001 not served, 9 ignored)", multi.ListenPort, multi.ListenPorts)
	}

	both := pollNodeConfig(t, router, bothSecret)
	if !reflect.DeepEqual(both.tags(), []string{"beta", "multi"}) {
		t.Fatalf("combined profile serves %v, want [beta multi]", both.tags())
	}
	one := both.inbound(t, "multi")
	if one.ListenPort != 20001 || one.ListenPorts != nil {
		t.Errorf("multi with one port left = port %d ports %v, want 20001 and no listen_ports", one.ListenPort, one.ListenPorts)
	}
	if both.inbound(t, "beta").ListenPort != 2087 {
		t.Errorf("beta port = %d", both.inbound(t, "beta").ListenPort)
	}
}

func TestNodeProfileCoreOverridesApplyToThatNodeOnly(t *testing.T) {
	router, token := newTestRouter(t)
	seedProfileFleet(t, router, token)
	if resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"log_level": "warn", "sniff_enabled": true,
		"outbounds":     []map[string]interface{}{{"tag": "exit-a", "type": "socks", "server": "1.1.1.1", "server_port": 1080}},
		"routing_rules": []map[string]interface{}{{"inbound": []string{"alpha"}, "outbound_tag": "exit-a"}},
		"dns_servers":   []map[string]interface{}{{"tag": "d1", "type": "udp", "address": "8.8.8.8"}},
	}); resp.Code != http.StatusOK {
		t.Fatalf("put core config: %d %v", resp.Code, resp.Body)
	}
	_, plainSecret := createProfileNode(t, router, token, "np-plain", nil)
	_, customSecret := createProfileNode(t, router, token, "np-custom", map[string]interface{}{
		"core_overrides": map[string]interface{}{
			"log_level": "debug", "sniff_enabled": false,
			"dns_servers": []map[string]interface{}{{"tag": "d2", "type": "tls", "address": "1.1.1.1"}},
			"outbounds": []map[string]interface{}{
				{"tag": "exit-a", "type": "socks", "server": "2.2.2.2", "server_port": 1081},
				{"tag": "exit-b", "type": "direct"},
			},
			"routing_rules_first": []map[string]interface{}{{"inbound": []string{"multi"}, "inbound_port": []int{20001}, "outbound_tag": "exit-b"}},
		},
	})

	custom := pollNodeConfig(t, router, customSecret)
	if custom.Core.LogLevel != "debug" || custom.Core.SniffEnabled {
		t.Errorf("custom core = %q sniff %v, want debug / false", custom.Core.LogLevel, custom.Core.SniffEnabled)
	}
	if len(custom.Core.DNSServers) != 1 || custom.Core.DNSServers[0].Tag != "d2" {
		t.Errorf("custom dns = %+v, want [d2]", custom.Core.DNSServers)
	}
	if len(custom.Core.Outbounds) != 2 || custom.Core.Outbounds[0].Tag != "exit-a" || custom.Core.Outbounds[0].Server != "2.2.2.2" || custom.Core.Outbounds[1].Tag != "exit-b" {
		t.Errorf("custom outbounds = %+v, want exit-a replaced in place then exit-b appended", custom.Core.Outbounds)
	}
	rules := custom.Core.RoutingRules
	if len(rules) != 2 || rules[0].OutboundTag != "exit-b" || !reflect.DeepEqual(rules[0].InboundPort, []int{20001}) || rules[1].OutboundTag != "exit-a" {
		t.Errorf("custom rules = %+v, want the node's rule first, the fleet's second", rules)
	}
	if !reflect.DeepEqual(custom.tags(), []string{"alpha", "beta", "multi"}) {
		t.Errorf("overrides alone must not change which inbounds are served, got %v", custom.tags())
	}

	plain := pollNodeConfig(t, router, plainSecret)
	if plain.Core.LogLevel != "warn" || !plain.Core.SniffEnabled || plain.Core.Outbounds[0].Server != "1.1.1.1" ||
		len(plain.Core.RoutingRules) != 1 || plain.Core.DNSServers[0].Tag != "d1" {
		t.Errorf("another node's core config changed: %+v", plain.Core)
	}
	if plain.Version == custom.Version {
		t.Error("different payloads must not share a version")
	}
}

func TestNodeProfileRejectsInvalidProfiles(t *testing.T) {
	router, token := newTestRouter(t)
	seedProfileFleet(t, router, token)
	if resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"log_level": "warn", "sniff_enabled": true,
		"outbounds": []map[string]interface{}{{"tag": "exit-a", "type": "socks", "server": "1.1.1.1", "server_port": 1080}},
	}); resp.Code != http.StatusOK {
		t.Fatalf("put core config: %d %v", resp.Code, resp.Body)
	}

	cases := []struct {
		name string
		body map[string]interface{}
		want string
	}{
		{"unknown inbound tag", map[string]interface{}{"inbound_tags": []string{"alpha", "ghost"}}, "inbound_tags: unknown inbound ghost"},
		{"duplicate inbound tag", map[string]interface{}{"inbound_tags": []string{"alpha", "alpha"}}, "inbound_tags: duplicate tag alpha"},
		{"blank inbound tag", map[string]interface{}{"inbound_tags": []string{" "}}, "inbound_tags: a tag must not be empty"},
		{"port zero", map[string]interface{}{"listen_ports": []int{0}}, "listen_ports: invalid port 0"},
		{"port too big", map[string]interface{}{"listen_ports": []int{70000}}, "listen_ports: invalid port 70000"},
		{"duplicate port", map[string]interface{}{"listen_ports": []int{443, 443}}, "listen_ports: duplicate port 443"},
		{"ports of the wrong type", map[string]interface{}{"listen_ports": "443"}, "listen_ports"},
		{"tags of the wrong type", map[string]interface{}{"inbound_tags": "alpha"}, "inbound_tags"},
		{"overrides not an object", map[string]interface{}{"core_overrides": []int{1}}, "must be a JSON object"},
		{"unknown override key", map[string]interface{}{"core_overrides": map[string]interface{}{"outbounds_typo": []int{}}}, "unknown field"},
		{"bad log level", map[string]interface{}{"core_overrides": map[string]interface{}{"log_level": "loud"}}, "invalid log_level"},
		{"rule to an unknown outbound", map[string]interface{}{"core_overrides": map[string]interface{}{
			"routing_rules_first": []map[string]interface{}{{"outbound_tag": "nope"}},
		}}, "routing rule targets unknown outbound: nope"},
		{"rule to an unknown inbound", map[string]interface{}{"core_overrides": map[string]interface{}{
			"routing_rules_first": []map[string]interface{}{{"inbound": []string{"ghost"}, "outbound_tag": "exit-a"}},
		}}, "routing rule targets unknown inbound: ghost"},
		{"reserved outbound tag", map[string]interface{}{"core_overrides": map[string]interface{}{
			"outbounds": []map[string]interface{}{{"tag": "direct", "type": "direct"}},
		}}, "reserved"},
		{"duplicate override outbound", map[string]interface{}{"core_overrides": map[string]interface{}{
			"outbounds": []map[string]interface{}{{"tag": "x", "type": "direct"}, {"tag": "x", "type": "direct"}},
		}}, "duplicate outbound tag: x"},
		{"outbound missing its server", map[string]interface{}{"core_overrides": map[string]interface{}{
			"outbounds": []map[string]interface{}{{"tag": "x", "type": "socks"}},
		}}, "server and server_port are required"},
	}
	for i, tc := range cases {
		body := map[string]interface{}{"name": "np-bad-" + strconv.Itoa(i), "address": "10.0.0.1"}
		for k, v := range tc.body {
			body[k] = v
		}
		resp := doRequest(t, router, "POST", "/api/node", token, body)
		detail, _ := resp.Body["detail"].(string)
		if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(detail, tc.want) {
			t.Errorf("%s: create = %d %q, want 422 mentioning %q", tc.name, resp.Code, detail, tc.want)
		}
	}
	var left []map[string]interface{}
	if list := doRequest(t, router, "GET", "/api/nodes", token, nil); json.Unmarshal(list.Raw, &left) != nil || len(left) != 0 {
		t.Errorf("a rejected create left a node behind: %s", list.Raw)
	}

	// The same checks on update, leaving the node untouched.
	id, _ := createProfileNode(t, router, token, "np-keep", map[string]interface{}{"listen_ports": []int{8443}})
	url := "/api/node/" + strconv.Itoa(int(id))
	resp := doRequest(t, router, "PUT", url, token, map[string]interface{}{"inbound_tags": []string{"ghost"}, "listen_ports": []int{9000}})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("update with an unknown tag: %d %v", resp.Code, resp.Body)
	}
	got := doRequest(t, router, "GET", url, token, nil)
	if !reflect.DeepEqual(got.Body["listen_ports"], []interface{}{float64(8443)}) || got.Body["inbound_tags"] != nil {
		t.Errorf("a rejected update changed the node: %v", got.Body)
	}
}

func TestNodeProfileFleetCoreConfigCannotBreakANodesOverrides(t *testing.T) {
	router, token := newTestRouter(t)
	seedProfileFleet(t, router, token)
	fleet := map[string]interface{}{
		"log_level": "warn", "sniff_enabled": true,
		"outbounds": []map[string]interface{}{{"tag": "exit-a", "type": "socks", "server": "1.1.1.1", "server_port": 1080}},
	}
	if resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, fleet); resp.Code != http.StatusOK {
		t.Fatalf("put core config: %d %v", resp.Code, resp.Body)
	}
	createProfileNode(t, router, token, "np-depends", map[string]interface{}{
		"core_overrides": map[string]interface{}{"routing_rules_first": []map[string]interface{}{{"outbound_tag": "exit-a"}}},
	})

	broken := map[string]interface{}{"log_level": "warn", "sniff_enabled": true}
	resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, broken)
	detail, _ := resp.Body["detail"].(string)
	if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(detail, "np-depends") || !strings.Contains(detail, "exit-a") {
		t.Fatalf("removing an outbound a node's rule targets: %d %q, want 422 naming the node and the outbound", resp.Code, detail)
	}
	if got := doRequest(t, router, "GET", "/api/settings/core-config", token, nil); len(got.Body["outbounds"].([]interface{})) != 1 {
		t.Errorf("the rejected fleet config was saved anyway: %v", got.Body)
	}
	// Replacing it with one that still defines the tag is fine.
	fleet["outbounds"] = []map[string]interface{}{{"tag": "exit-a", "type": "socks", "server": "9.9.9.9", "server_port": 1080}}
	if resp := doRequest(t, router, "PUT", "/api/settings/core-config", token, fleet); resp.Code != http.StatusOK {
		t.Errorf("a compatible fleet update was rejected: %d %v", resp.Code, resp.Body)
	}
}

func TestNodeProfileDTORoundTrip(t *testing.T) {
	router, token := newTestRouter(t)
	seedProfileFleet(t, router, token)
	overrides := map[string]interface{}{"log_level": "debug"}
	id, _ := createProfileNode(t, router, token, "np-dto", map[string]interface{}{
		"inbound_tags": []string{"multi", "alpha"}, "listen_ports": []int{20002, 8443}, "core_overrides": overrides,
	})
	url := "/api/node/" + strconv.Itoa(int(id))

	check := func(label string, body map[string]interface{}, tags, ports []interface{}, ov interface{}) {
		t.Helper()
		if !reflect.DeepEqual(body["inbound_tags"], asAny(tags)) || !reflect.DeepEqual(body["listen_ports"], asAny(ports)) || !reflect.DeepEqual(body["core_overrides"], ov) {
			t.Errorf("%s: inbound_tags=%v listen_ports=%v core_overrides=%v, want %v %v %v", label,
				body["inbound_tags"], body["listen_ports"], body["core_overrides"], tags, ports, ov)
		}
	}

	create := doRequest(t, router, "GET", url, token, nil)
	check("get", create.Body, []interface{}{"alpha", "multi"}, []interface{}{float64(8443), float64(20002)}, map[string]interface{}{"log_level": "debug"})

	list := doRequest(t, router, "GET", "/api/nodes", token, nil)
	var nodes []map[string]interface{}
	if err := json.Unmarshal(list.Raw, &nodes); err != nil || len(nodes) != 1 {
		t.Fatalf("list nodes: %v %s", err, list.Raw)
	}
	check("list", nodes[0], []interface{}{"alpha", "multi"}, []interface{}{float64(8443), float64(20002)}, map[string]interface{}{"log_level": "debug"})

	// A partial update leaves the profile alone.
	up := doRequest(t, router, "PUT", url, token, map[string]interface{}{"usage_coefficient": 2})
	if up.Code != http.StatusOK || up.Body["usage_coefficient"] != float64(2) {
		t.Fatalf("partial update: %d %v", up.Code, up.Body)
	}
	check("after partial update", up.Body, []interface{}{"alpha", "multi"}, []interface{}{float64(8443), float64(20002)}, map[string]interface{}{"log_level": "debug"})

	// null clears exactly that field.
	up = doRequest(t, router, "PUT", url, token, map[string]interface{}{"listen_ports": nil})
	check("ports cleared", up.Body, []interface{}{"alpha", "multi"}, nil, map[string]interface{}{"log_level": "debug"})

	// So does an empty value; a new value replaces.
	up = doRequest(t, router, "PUT", url, token, map[string]interface{}{"inbound_tags": []string{"beta"}, "core_overrides": map[string]interface{}{}})
	check("tags replaced, overrides emptied", up.Body, []interface{}{"beta"}, nil, nil)
	up = doRequest(t, router, "PUT", url, token, map[string]interface{}{"inbound_tags": []string{}})
	check("tags emptied", up.Body, nil, nil, nil)
	for _, k := range []string{"inbound_tags", "listen_ports", "core_overrides"} {
		if _, has := up.Body[k]; has {
			t.Errorf("a node back on the default profile still carries %q", k)
		}
	}

	// The create response carries them too, at the top level and nested.
	created := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{
		"name": "np-dto-2", "address": "10.0.0.2", "listen_ports": []int{443},
	})
	if !reflect.DeepEqual(created.Body["listen_ports"], []interface{}{float64(443)}) ||
		!reflect.DeepEqual(created.Body["node"].(map[string]interface{})["listen_ports"], []interface{}{float64(443)}) {
		t.Errorf("create response lacks listen_ports: %v", created.Body)
	}
}

func asAny(v []interface{}) interface{} {
	if v == nil {
		return nil
	}
	return v
}

func TestNodeConfigNodesWithTheSameProfileShareOneBuild(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	seedProfileFleet(t, router, token)
	_, a1 := createProfileNode(t, router, token, "np-a1", map[string]interface{}{"inbound_tags": []string{"alpha", "beta"}})
	_, a2 := createProfileNode(t, router, token, "np-a2", map[string]interface{}{"inbound_tags": []string{"beta", "alpha"}})
	_, d1 := createProfileNode(t, router, token, "np-d1", nil)
	_, d2 := createProfileNode(t, router, token, "np-d2", nil)

	var bodies [][]byte
	var etags []string
	for round := 0; round < 2; round++ {
		for _, secret := range []string{a1, a2, d1, d2} {
			code, h, body := getNodeConfigRaw(t, router, secret, "")
			if code != http.StatusOK {
				t.Fatalf("poll: %d %s", code, body)
			}
			if round == 0 {
				bodies = append(bodies, body)
				etags = append(etags, h.Get("ETag"))
			}
		}
	}
	if got := handler.nodeConfig.renders.Load(); got != 2 {
		t.Errorf("rendered %d payloads for 2 distinct profiles polled by 4 nodes twice, want 2", got)
	}
	if got := handler.nodeConfig.snapshotLoads.Load(); got != 1 {
		t.Errorf("loaded the fleet snapshot %d times, want once however many profiles ask", got)
	}
	if string(bodies[0]) != string(bodies[1]) || etags[0] != etags[1] {
		t.Error("the two nodes with the same tag set were served different payloads")
	}
	if string(bodies[2]) != string(bodies[3]) || etags[2] != etags[3] {
		t.Error("the two default nodes were served different payloads")
	}
	if etags[0] == etags[2] {
		t.Error("distinct profiles share an ETag")
	}
}

func TestNodeConfigETagAnd304(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	seedProfileFleet(t, router, token)
	_, secret := createProfileNode(t, router, token, "np-etag", nil)

	code, header, body := getNodeConfigRaw(t, router, secret, "")
	etag := header.Get("ETag")
	if code != http.StatusOK || etag == "" || len(body) == 0 {
		t.Fatalf("first poll: %d etag=%q body=%d bytes", code, etag, len(body))
	}
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || etag != `"`+decodeNodeCfg(t, body).Version+`"` {
		t.Fatalf("ETag %q is not the quoted payload version", etag)
	}

	for _, inm := range []string{etag, `W/` + etag, `"nope", ` + etag, `*`} {
		code, h, body := getNodeConfigRaw(t, router, secret, inm)
		if code != http.StatusNotModified || len(body) != 0 {
			t.Errorf("If-None-Match %q: %d with %d body bytes, want an empty 304", inm, code, len(body))
		}
		if h.Get("ETag") != etag {
			t.Errorf("If-None-Match %q: 304 ETag = %q, want %q", inm, h.Get("ETag"), etag)
		}
	}
	code, _, body = getNodeConfigRaw(t, router, secret, `"some-older-version"`)
	if code != http.StatusOK || len(body) == 0 {
		t.Errorf("a stale If-None-Match must get the full body, got %d with %d bytes", code, len(body))
	}
	if code, _, _ := getNodeConfigRaw(t, router, secret, ""); code != http.StatusOK {
		t.Errorf("a poll with no If-None-Match must keep getting the full body, got %d", code)
	}

	// A rebuild that yields identical content is still a 304 for that ETag.
	before := handler.nodeConfig.snapshotLoads.Load()
	if err := handler.store.InvalidateNodeConfigPayload(context.Background()); err != nil {
		t.Fatalf("InvalidateNodeConfigPayload: %v", err)
	}
	code, _, body = getNodeConfigRaw(t, router, secret, etag)
	if code != http.StatusNotModified || len(body) != 0 {
		t.Errorf("after an invalidation that changed nothing: %d, want 304", code)
	}
	if handler.nodeConfig.snapshotLoads.Load() != before+1 {
		t.Error("InvalidateNodeConfigPayload did not make the next poll rebuild")
	}

	// A real change ends the 304s.
	doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
		"alpha": []map[string]interface{}{{"remark": "a", "address": "1.2.3.4", "port": 8444}},
		"beta":  []map[string]interface{}{{"remark": "b", "address": "1.2.3.4", "port": 2087}},
		"multi": multiPortHosts(),
	})
	code, h, body := getNodeConfigRaw(t, router, secret, etag)
	if code != http.StatusOK || h.Get("ETag") == etag || decodeNodeCfg(t, body).inbound(t, "alpha").ListenPort != 8444 {
		t.Errorf("after a host change: %d etag %q, want 200 with a new ETag and the new port", code, h.Get("ETag"))
	}
}

func TestNodeConfigDoesNotRebuildUntilSomethingItServesChanges(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	pool := handler.store.Pool
	seedProfileFleet(t, router, token)
	id, secret := createProfileNode(t, router, token, "np-rebuild", nil)
	url := "/api/node/" + strconv.Itoa(int(id))

	loads := func() int64 { return handler.nodeConfig.snapshotLoads.Load() }
	renders := func() int64 { return handler.nodeConfig.renders.Load() }

	first := pollNodeConfig(t, router, secret)
	if loads() != 1 || renders() != 1 {
		t.Fatalf("first poll: %d snapshot loads / %d renders, want 1 / 1", loads(), renders())
	}
	for i := 0; i < 15; i++ {
		if pollNodeConfig(t, router, secret).Version != first.Version {
			t.Fatal("the payload changed with nothing changing")
		}
	}
	if loads() != 1 || renders() != 1 {
		t.Fatalf("15 polls of an unchanged database rebuilt: %d snapshot loads / %d renders, want 1 / 1", loads(), renders())
	}

	// Traffic and liveness writes are constant and must never cause a rebuild.
	if resp := doRequest(t, router, "POST", "/api/internal/node-report", secret, nodeReportPayload([]map[string]interface{}{
		{"username": "pp_user_a", "uplink": 100, "downlink": 200},
	})); resp.Code != http.StatusOK {
		t.Fatalf("node report: %d %v", resp.Code, resp.Body)
	}
	mustExec(t, pool, `UPDATE users SET used_traffic = used_traffic + 7, online_at = now()`)
	mustExec(t, pool, `UPDATE nodes SET uplink = uplink + 1, downlink = downlink + 1, status = 'connected', last_status_change = now()`)
	pollNodeConfig(t, router, secret)
	if loads() != 1 || renders() != 1 {
		t.Fatalf("usage-only writes caused a rebuild: %d snapshot loads / %d renders, want 1 / 1", loads(), renders())
	}

	changes := []struct {
		name  string
		do    func()
		check func(v nodeCfgView) bool
	}{
		{"host", func() {
			doRequest(t, router, "PUT", "/api/hosts", token, map[string]interface{}{
				"alpha": []map[string]interface{}{{"remark": "a", "address": "1.2.3.4", "port": 8444}},
				"beta":  []map[string]interface{}{{"remark": "b", "address": "1.2.3.4", "port": 2087}},
				"multi": multiPortHosts(),
			})
		}, func(v nodeCfgView) bool { return v.inbound(t, "alpha").ListenPort == 8444 }},
		{"proxy", func() {
			mustExec(t, pool, `UPDATE proxies SET settings = jsonb_set(settings, '{id}', '"11111111-2222-4333-8444-555555555555"') WHERE type = 'vless'`)
		}, func(v nodeCfgView) bool {
			return v.inbound(t, "alpha").Users[0].UUID == "11111111-2222-4333-8444-555555555555"
		}},
		{"user status", func() {
			doRequest(t, router, "PUT", "/api/user/pp_user_a", token, map[string]interface{}{"status": "disabled"})
		}, func(v nodeCfgView) bool { return len(v.inbound(t, "alpha").Users) == 0 }},
		{"node overrides", func() {
			doRequest(t, router, "PUT", url, token, map[string]interface{}{"core_overrides": map[string]interface{}{"log_level": "debug"}})
		}, func(v nodeCfgView) bool { return v.Core.LogLevel == "debug" }},
		{"node tags", func() {
			doRequest(t, router, "PUT", url, token, map[string]interface{}{"inbound_tags": []string{"beta"}})
		}, func(v nodeCfgView) bool { return reflect.DeepEqual(v.tags(), []string{"beta"}) }},
		{"core config", func() {
			doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{"log_level": "error", "sniff_enabled": true})
		}, func(v nodeCfgView) bool { return v.Core.LogLevel == "debug" && v.Core.SniffEnabled }}, // this node's override still wins
	}
	for _, ch := range changes {
		before := loads()
		ch.do()
		v := pollNodeConfig(t, router, secret)
		if loads() != before+1 {
			t.Errorf("%s: the very next poll rebuilt %d times, want exactly 1", ch.name, loads()-before)
		}
		if !ch.check(v) {
			t.Errorf("%s: the very next poll did not reflect the change: %+v", ch.name, v)
		}
		pollNodeConfig(t, router, secret)
		if loads() != before+1 {
			t.Errorf("%s: a second poll rebuilt again", ch.name)
		}
	}
}

func mustExec(t testing.TB, pool *pgxpool.Pool, sql string, args ...interface{}) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func readDataVersion(t testing.TB, pool *pgxpool.Pool) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(context.Background(), `SELECT version FROM data_versions WHERE name = 'node_config'`).Scan(&v); err != nil {
		t.Fatalf("read data version: %v", err)
	}
	return v
}

// bumpsVersion runs stmt in a transaction it then rolls back and reports
// whether the node_config version moved. Unlike firedTriggers it also works
// for TRUNCATE, which EXPLAIN cannot take.
func bumpsVersion(t testing.TB, pool *pgxpool.Pool, stmt string) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	read := func() int64 {
		var v int64
		if err := tx.QueryRow(ctx, `SELECT version FROM data_versions WHERE name = 'node_config'`).Scan(&v); err != nil {
			t.Fatalf("read version: %v", err)
		}
		return v
	}
	before := read()
	if _, err := tx.Exec(ctx, stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
	return read() != before
}

var triggerCalls = regexp.MustCompile(`Trigger (node_config_version\w+).*calls=(\d+)`)

// firedTriggers runs stmt under EXPLAIN ANALYZE inside a transaction that is
// rolled back and reports which node_config_version triggers it fired, with
// their call counts - the database's own account of what the statement does,
// rather than an inference from before/after values.
func firedTriggers(t testing.TB, pool *pgxpool.Pool, stmt string) map[string]int {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, TIMING OFF) "+stmt)
	if err != nil {
		t.Fatalf("explain %s: %v", stmt, err)
	}
	defer rows.Close()
	fired := map[string]int{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if m := triggerCalls.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			fired[m[1]] += n
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain %s: %v", stmt, err)
	}
	return fired
}

func TestNodeConfigVersionTriggersFireForExactlyTheRightWrites(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	pool := handler.store.Pool
	seedProfileFleet(t, router, token)
	createProfileNode(t, router, token, "np-trig-1", nil)
	createProfileNode(t, router, token, "np-trig-2", nil)
	for _, name := range []string{"pp_user_b", "pp_user_c"} {
		doRequest(t, router, "POST", "/api/user", token, map[string]interface{}{
			"username": name, "proxies": map[string]interface{}{"vless": map[string]interface{}{}},
		})
	}

	bumps := []string{
		`INSERT INTO hosts (remark, address, port, inbound_tag) VALUES ('x', '1.1.1.1', 9000, 'alpha')`,
		`UPDATE hosts SET remark = remark`,
		`DELETE FROM hosts WHERE remark = 'a'`,
		`UPDATE inbounds SET network = network`,
		`DELETE FROM inbounds WHERE tag = 'beta'`,
		`UPDATE core_config SET log_level = 'info'`,
		`INSERT INTO proxies (user_id, type, settings) SELECT id, 'trojan', '{"password":"p"}' FROM users LIMIT 1`,
		`UPDATE proxies SET settings = settings`,
		`DELETE FROM proxies WHERE type = 'trojan'`,
		`INSERT INTO users (username) VALUES ('sql_user')`,
		`DELETE FROM users WHERE username = 'pp_user_b'`,
		`UPDATE users SET status = 'disabled' WHERE username = 'pp_user_b'`,
		`UPDATE users SET username = username || '_x'`,
		`INSERT INTO nodes (name, address, port, api_port) VALUES ('np-sql', '1.1.1.1', 1, 2)`,
		`DELETE FROM nodes WHERE name = 'np-trig-1'`,
		`UPDATE nodes SET name = name || '_x'`,
		`UPDATE nodes SET inbound_tags = ARRAY['alpha']`,
		`UPDATE nodes SET listen_ports = ARRAY[443]`,
		`UPDATE nodes SET core_overrides = '{"log_level": "debug"}'`,
		`TRUNCATE hosts`,
		`TRUNCATE nodes CASCADE`,
	}
	for _, stmt := range bumps {
		if !bumpsVersion(t, pool, stmt) {
			t.Errorf("%s\n  did not bump the node_config version, but a node can be served something different because of it", stmt)
		}
	}

	quiet := []string{
		`UPDATE users SET used_traffic = used_traffic + 5, online_at = now()`,
		`UPDATE users SET used_traffic = used_traffic + v.d, online_at = now() FROM (SELECT id, 3::bigint AS d FROM users) v WHERE users.id = v.id`,
		`UPDATE users SET note = 'n', edit_at = now(), sub_updated_at = now(), sub_last_user_agent = 'ua', data_limit = 5, expire = 9, last_status_change = now()`,
		`UPDATE nodes SET uplink = uplink + 1`,
		`UPDATE nodes SET uplink = uplink + 1, downlink = downlink + 1`,
		`UPDATE nodes SET status = 'connected', last_status_change = now(), message = 'm', xray_version = '1', usage_coefficient = 2, address = 'z', port = 5, api_port = 6, report_secret = report_secret || 'r'`,
		// The load indicator's per-node capacity never changes what a node runs.
		`UPDATE nodes SET capacity = 15000`,
		`UPDATE nodes SET capacity = NULL`,
	}
	for _, stmt := range quiet {
		if bumpsVersion(t, pool, stmt) {
			t.Errorf("%s\n  bumped the version; a write that happens every few seconds must not", stmt)
		}
		if fired := firedTriggers(t, pool, stmt); len(fired) != 0 {
			t.Errorf("%s\n  fired %v; a write that happens every few seconds must never bump the version", stmt, fired)
		}
	}

	// Statement-level: a bulk write bumps once, however many rows it touches.
	if fired := firedTriggers(t, pool, `UPDATE users SET status = 'disabled'`); fired["node_config_version_users_identity"] != 1 {
		t.Errorf("bulk UPDATE of 3 users fired %v, want the users trigger exactly once", fired)
	}
	if fired := firedTriggers(t, pool, `DELETE FROM users`); fired["node_config_version_users_rows"] != 1 {
		t.Errorf("bulk DELETE of users fired %v, want the users trigger exactly once", fired)
	}

	// And the counter itself: one number, only ever up, and the version
	// after a bump can never repeat an earlier one even if it was rolled back.
	v1 := readDataVersion(t, pool)
	mustExec(t, pool, `UPDATE core_config SET log_level = 'warn'`)
	v2 := readDataVersion(t, pool)
	if v2 <= v1 {
		t.Errorf("version %d -> %d after a core_config write, want it to grow", v1, v2)
	}
	mustExec(t, pool, `UPDATE users SET used_traffic = used_traffic + 1, online_at = now()`)
	mustExec(t, pool, `UPDATE nodes SET uplink = uplink + 1`)
	if v3 := readDataVersion(t, pool); v3 != v2 {
		t.Errorf("version moved %d -> %d on usage-only writes", v2, v3)
	}
	mustExec(t, pool, `UPDATE data_versions SET version = 0 WHERE name = 'node_config'`)
	mustExec(t, pool, `UPDATE core_config SET log_level = 'warn'`)
	if rolledBack := readDataVersion(t, pool); rolledBack <= v1 {
		t.Errorf("after the counter was reset to 0 the next bump gave %d, want a wall-clock based value above the earlier %d", rolledBack, v1)
	}
}

func TestNodeConfigStaysCorrectWhenTheVersionCannotBeRead(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	seedProfileFleet(t, router, token)
	_, secret := createProfileNode(t, router, token, "np-noversion", nil)
	pollNodeConfig(t, router, secret)

	handler.store.Queries = generated.New(versionlessDB{handler.store.Pool})
	before := handler.nodeConfig.snapshotLoads.Load()
	for i := 0; i < 3; i++ {
		v := pollNodeConfig(t, router, secret)
		if !reflect.DeepEqual(v.tags(), []string{"alpha", "beta", "multi"}) {
			t.Fatalf("poll without a readable version served %v", v.tags())
		}
	}
	if got := handler.nodeConfig.snapshotLoads.Load() - before; got != 3 {
		t.Errorf("3 polls without a readable version loaded the snapshot %d times, want a fresh build each (3)", got)
	}
	code, header, body := getNodeConfigRaw(t, router, secret, "")
	if code != http.StatusOK || len(body) == 0 || header.Get("ETag") == "" {
		t.Errorf("fallback answered %d with an ETag %q", code, header.Get("ETag"))
	}
}

// versionlessDB is a database in which the data_versions read fails.
type versionlessDB struct{ generated.DBTX }

func (d versionlessDB) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if strings.Contains(sql, "FROM data_versions") {
		return failingRow{}
	}
	return d.DBTX.QueryRow(ctx, sql, args...)
}

type failingRow struct{}

func (failingRow) Scan(...interface{}) error { return errors.New("data_versions unreadable") }

func TestNodeConfigConcurrentPollsShareOneRebuild(t *testing.T) {
	router, token, handler := newTestRouterAndHandler(t)
	seedProfileFleet(t, router, token)
	_, secret := createProfileNode(t, router, token, "np-concurrent", nil)

	const pollers = 12
	versions := make(chan string, pollers)
	start := make(chan struct{})
	for i := 0; i < pollers; i++ {
		go func() {
			<-start
			code, _, body := getNodeConfigRaw(t, router, secret, "")
			if code != http.StatusOK {
				versions <- "status " + strconv.Itoa(code)
				return
			}
			var v struct {
				Version string `json:"version"`
			}
			_ = json.Unmarshal(body, &v)
			versions <- v.Version
		}()
	}
	close(start)
	seen := []string{}
	for i := 0; i < pollers; i++ {
		seen = append(seen, <-versions)
	}
	sort.Strings(seen)
	if seen[0] != seen[len(seen)-1] || strings.HasPrefix(seen[0], "status") {
		t.Fatalf("concurrent polls disagreed or failed: %v", seen)
	}
	if got := handler.nodeConfig.snapshotLoads.Load(); got != 1 {
		t.Errorf("%d simultaneous polls loaded the snapshot %d times, want the rebuild collapsed to 1", pollers, got)
	}
	if got := handler.nodeConfig.renders.Load(); got != 1 {
		t.Errorf("%d simultaneous polls rendered %d payloads, want 1", pollers, got)
	}
}
