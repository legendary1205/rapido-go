package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/loadmap"
	"github.com/legendary1205/rapido-go/internal/subscription"
)

// --- unit tests on the remark / ordering rules (no database) -----------------

type loadFakeInventory struct {
	nodes []loadmap.Node
	hosts []loadmap.Host
}

func (f loadFakeInventory) Nodes(context.Context) ([]loadmap.Node, error) { return f.nodes, nil }
func (f loadFakeInventory) Hosts(context.Context) ([]loadmap.Host, error) { return f.hosts, nil }

type loadFakePresence struct{ data loadmap.PresenceData }

func (f loadFakePresence) Read(context.Context, []int32) (loadmap.PresenceData, error) {
	return f.data, nil
}

// loadHandler is a Handler whose load map knows one node with the given open
// connections per port; a nil ports map means presence is not live at all.
func loadHandler(indicator, sortByLoad bool, hosts []loadmap.Host, ports map[int]int) *Handler {
	pd := loadmap.PresenceData{}
	if ports != nil {
		pd = loadmap.PresenceData{Live: true, Nodes: map[int32]loadmap.NodePresence{1: {Reporting: true, Ports: ports}}}
	}
	m := loadmap.New(loadmap.Config{Capacity: 1000},
		loadFakeInventory{nodes: []loadmap.Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}}, hosts: hosts},
		loadFakePresence{data: pd})
	return &Handler{loads: m, loadIndicator: indicator, loadCapacity: 1000, sortByLoad: sortByLoad}
}

func testVars() subscription.Variables {
	return subscription.BuildVariables(subscription.UserInfo{Username: "alice", Status: "active"}, "203.0.113.1")
}

func TestRequestLoadRemarkRules(t *testing.T) {
	hosts := []loadmap.Host{
		{ID: 1, Remark: "🇩🇪 Germany", Address: "10.0.0.1", Port: 20001},
		{ID: 2, Remark: "🛜 {DATA_LEFT} 🛜", Address: "10.0.0.1", Port: 20001},
		{ID: 3, Remark: "Explicit {LOAD_EMOJI} {LOAD_LEVEL}", Address: "10.0.0.1", Port: 20001},
		{ID: 4, Remark: "Both {LOAD} {LOAD_PERCENT}", Address: "10.0.0.1", Port: 20001},
		{ID: 5, Remark: "Web {TRANSPORT}", Address: "10.0.0.1", Port: 20001},
		{ID: 6, Remark: "🇫🇷 France", Address: "10.0.0.1", Port: 20009},
	}
	remark := func(h *Handler, id int32, template string) string {
		r := &requestLoad{h: h, ctx: context.Background()}
		vars := testVars()
		vars["TRANSPORT"] = "tcp"
		out, _, _ := r.remark(vars, generated.Host{ID: id, Remark: template})
		return out
	}

	on := loadHandler(true, false, hosts, map[int]int{20001: 230, 20009: 950})
	off := loadHandler(false, false, hosts, map[int]int{20001: 230, 20009: 950})
	dark := loadHandler(true, false, hosts, nil)

	cases := []struct {
		name string
		h    *Handler
		id   int32
		tmpl string
		want string
	}{
		{"plain remark gets the suffix", on, 1, "🇩🇪 Germany", "🇩🇪 Germany 🟢 23%"},
		{"a full config is red", on, 6, "🇫🇷 France", "🇫🇷 France 🔴 95%"},
		{"info host is left alone", on, 2, "🛜 {DATA_LEFT} 🛜", "🛜 ∞ 🛜"},
		{"other variables never get a suffix", on, 5, "Web {TRANSPORT}", "Web tcp"},
		{"explicit variables are used as written", on, 3, "Explicit {LOAD_EMOJI} {LOAD_LEVEL}", "Explicit 🟢 free"},
		{"two explicit variables, no auto suffix", on, 4, "Both {LOAD} {LOAD_PERCENT}", "Both 🟢 23% 23%"},
		{"indicator off leaves a plain remark untouched", off, 1, "🇩🇪 Germany", "🇩🇪 Germany"},
		{"indicator off still renders an explicit variable", off, 4, "Both {LOAD} {LOAD_PERCENT}", "Both 🟢 23% 23%"},
		{"no presence: plain remark untouched", dark, 1, "🇩🇪 Germany", "🇩🇪 Germany"},
		{"no presence: explicit variable renders empty and the space is trimmed", dark, 4, "Both {LOAD} {LOAD_PERCENT}", "Both"},
		{"no presence: info host unchanged", dark, 2, "🛜 {DATA_LEFT} 🛜", "🛜 ∞ 🛜"},
		{"a host the snapshot does not know", on, 99, "Brand new", "Brand new"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := remark(c.h, c.id, c.tmpl); got != c.want {
				t.Errorf("remark = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRequestLoadSharedVariablesDoNotLeakBetweenHosts(t *testing.T) {
	h := loadHandler(true, false, []loadmap.Host{
		{ID: 1, Remark: "A", Address: "10.0.0.1", Port: 20001},
		{ID: 2, Remark: "B {LOAD}", Address: "10.0.0.1", Port: 20002},
	}, map[int]int{20001: 950}) // 20002 is idle
	r := &requestLoad{h: h, ctx: context.Background()}
	vars := testVars()

	if got, _, _ := r.remark(vars, generated.Host{ID: 1, Remark: "A"}); got != "A 🔴 95%" {
		t.Errorf("host 1 = %q", got)
	}
	// Host 2 is the same request, same shared map: its own 0% must show, not host 1's 95%.
	if got, _, _ := r.remark(vars, generated.Host{ID: 2, Remark: "B {LOAD}"}); got != "B 🟢 0%" {
		t.Errorf("host 2 = %q", got)
	}
	// And a host with no data at all must not inherit host 2's.
	if got, _, _ := r.remark(vars, generated.Host{ID: 3, Remark: "C {LOAD}"}); got != "C" {
		t.Errorf("host 3 = %q, want it empty and trimmed", got)
	}
}

func TestRequestLoadOnlyTouchesTheLoadMapWhenNeeded(t *testing.T) {
	// Indicator and sorting off and no {LOAD} in the template: the map is never
	// asked for a snapshot, so a nil map is fine and nothing is fetched.
	h := &Handler{loadCapacity: 1000}
	r := &requestLoad{h: h, ctx: context.Background()}
	if got, _, sortable := r.remark(testVars(), generated.Host{ID: 1, Remark: "Plain"}); got != "Plain" || sortable {
		t.Errorf("remark = %q sortable=%v", got, sortable)
	}
	if r.fetched {
		t.Error("the snapshot must not be fetched when nothing needs it")
	}
}

func TestSortPendingByLoadKeepsInfoHostsInPlace(t *testing.T) {
	mk := func(remark string, key int, sortable bool) pendingHost {
		return pendingHost{remark: remark, sortKey: key, sortable: sortable}
	}
	items := []pendingHost{
		mk("A80", 80, true),
		mk("INFO1", 0, false),
		mk("B10", 10, true),
		mk("C10", 10, true), // ties keep their incoming (priority) order
		mk("INFO2", 0, false),
		mk("D-unknown", unknownLoadSortKey, true),
		mk("E5", 5, true),
	}
	sortPendingByLoad(items)
	var got []string
	for _, it := range items {
		got = append(got, it.remark)
	}
	want := []string{"E5", "INFO1", "B10", "C10", "INFO2", "A80", "D-unknown"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}

	// Fewer than two sortable hosts: nothing moves.
	items = []pendingHost{mk("X", 50, true), mk("INFO", 0, false)}
	sortPendingByLoad(items)
	if items[0].remark != "X" || items[1].remark != "INFO" {
		t.Errorf("a single sortable host must not move: %v", items)
	}
}

func TestPeerRemarkNeverCarriesALoad(t *testing.T) {
	vars := testVars()
	vars.SetLoad("🔴", "97%", "full") // left over from a previous local host
	if got := peerRemark(vars, "nl", "🇳🇱 Amsterdam"); got != "[nl] 🇳🇱 Amsterdam" {
		t.Errorf("plain peer remark = %q", got)
	}
	if got := peerRemark(vars, "nl", "🇳🇱 Amsterdam {LOAD}"); got != "[nl] 🇳🇱 Amsterdam" {
		t.Errorf("a {LOAD} in a peer template must render empty, got %q", got)
	}
	if got := peerRemark(vars, "nl", "Hi {USERNAME}"); got != "[nl] Hi alice" {
		t.Errorf("peer remark with a user variable = %q", got)
	}
}

// --- against the real database and Redis -------------------------------------

type loadFixture struct {
	t      *testing.T
	router http.Handler
	token  string
	h      *Handler
	nodes  map[string]int32 // address -> node id
}

// presenceTestTTL outlives any slow test run over the shared test database's
// tunnel; production keys live 20 s.
const presenceTestTTL = 10 * time.Minute

// newLoadFixture creates the inbounds "main" (vless), "vm" (vmess over
// websocket, needed for the Clash formats) and "ss" (shadowsocks, for Outline),
// and two nodes (10.0.0.1, 10.0.0.2).
func newLoadFixture(t *testing.T) *loadFixture {
	t.Helper()
	router, token, h := newTestRouterAndHandler(t)
	fx := &loadFixture{t: t, router: router, token: token, h: h, nodes: map[string]int32{}}

	doRequest(t, router, "POST", "/api/inbounds/sync", token, []map[string]interface{}{
		{"tag": "main", "protocol": "vless", "network": "tcp", "security": "none"},
		{"tag": "vm", "protocol": "vmess", "network": "ws", "security": "tls"},
		{"tag": "ss", "protocol": "shadowsocks"},
	})
	for i, addr := range []string{"10.0.0.1", "10.0.0.2"} {
		resp := doRequest(t, router, "POST", "/api/node", token, map[string]interface{}{
			"name": "load-node-" + strconv.Itoa(i+1), "address": addr, "port": 443, "api_port": 62051,
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("create node %s: %d %v", addr, resp.Code, resp.Body)
		}
		fx.nodes[addr] = int32(resp.Body["id"].(float64))
	}
	return fx
}

// putHosts replaces the hosts of the "main" inbound.
func (fx *loadFixture) putHosts(hosts ...map[string]interface{}) {
	fx.t.Helper()
	fx.putHostsByTag(map[string][]map[string]interface{}{"main": hosts})
}

func (fx *loadFixture) putHostsByTag(byTag map[string][]map[string]interface{}) {
	fx.t.Helper()
	body := map[string]interface{}{}
	for tag, hosts := range byTag {
		body[tag] = hosts
	}
	resp := doRequest(fx.t, fx.router, "PUT", "/api/hosts", fx.token, body)
	if resp.Code != http.StatusOK {
		fx.t.Fatalf("put hosts: %d %s", resp.Code, resp.Raw)
	}
}

func lhost(remark, address string, port, priority int) map[string]interface{} {
	return map[string]interface{}{"remark": remark, "address": address, "port": port, "security": "none", "priority": priority}
}

// setPresence writes what node-live would have written straight into Redis
// (presence:live, and per node the total key and the ports hash), then swaps in
// a fresh load map so the next request rebuilds its snapshot instead of waiting
// out the 3 s cache.
func (fx *loadFixture) setPresence(live bool, ports map[string]map[string]interface{}) {
	fx.t.Helper()
	ctx := context.Background()
	rdb := fx.h.store.Cache.Raw()
	if live {
		if err := rdb.Set(ctx, "presence:live", "1", presenceTestTTL).Err(); err != nil {
			fx.t.Fatalf("set presence:live: %v", err)
		}
	}
	for addr, p := range ports {
		id := fx.nodes[addr]
		total := 0
		for _, v := range p {
			total += v.(int)
		}
		rdb.Set(ctx, "presence:node:"+strconv.Itoa(int(id))+":total", total, presenceTestTTL)
		if len(p) > 0 {
			rdb.HSet(ctx, "presence:node:"+strconv.Itoa(int(id))+":ports", p)
			rdb.Expire(ctx, "presence:node:"+strconv.Itoa(int(id))+":ports", presenceTestTTL)
		}
	}
	fx.h.WithConfigLoad(fx.h.loadIndicator, fx.h.loadCapacity, fx.h.sortByLoad)
}

// createUser makes a user with the given proxy protocols (vless by default) and
// returns the token of their subscription.
func (fx *loadFixture) createUser(name string, protocols ...string) string {
	fx.t.Helper()
	if len(protocols) == 0 {
		protocols = []string{"vless"}
	}
	proxies := map[string]interface{}{}
	for _, p := range protocols {
		proxies[p] = map[string]interface{}{}
	}
	resp := doRequest(fx.t, fx.router, "POST", "/api/user", fx.token, map[string]interface{}{
		"username": name, "proxies": proxies,
	})
	if resp.Code != http.StatusOK {
		fx.t.Fatalf("create user: %d %v", resp.Code, resp.Body)
	}
	return subscription.CreateToken(name, []byte(testSubSecret))
}

// linkRemarks fetches the user's v2ray subscription and returns every link's
// decoded remark (the URL fragment), in order.
func (fx *loadFixture) linkRemarks(subToken string) []string {
	fx.t.Helper()
	resp := doRequest(fx.t, fx.router, "GET", "/sub/"+subToken, "", nil)
	if resp.Code != http.StatusOK {
		fx.t.Fatalf("subscription: %d %s", resp.Code, resp.Raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(resp.Raw))
	if err != nil {
		fx.t.Fatalf("subscription is not base64: %v", err)
	}
	var remarks []string
	for _, link := range strings.Split(strings.TrimSpace(string(decoded)), "\n") {
		i := strings.LastIndex(link, "#")
		if i < 0 {
			fx.t.Fatalf("link without a remark: %s", link)
		}
		remark, err := url.PathUnescape(link[i+1:])
		if err != nil {
			fx.t.Fatalf("bad remark escaping in %s: %v", link, err)
		}
		remarks = append(remarks, remark)
	}
	return remarks
}

func TestSubscriptionLinkCarriesTheLoadSuffix(t *testing.T) {
	fx := newLoadFixture(t)
	fx.putHosts(
		lhost("🇩🇪 Germany", "10.0.0.1", 20001, 0),
		lhost("🛜 {DATA_LEFT} 🛜", "10.0.0.1", 20001, 1),
		lhost("🇳🇱 Netherlands", "10.0.0.2", 20001, 2),
	)
	fx.setPresence(true, map[string]map[string]interface{}{
		"10.0.0.1": {"20001": 230},
		"10.0.0.2": {"20001": 640},
	})
	sub := fx.createUser("load_link_user")

	got := fx.linkRemarks(sub)
	want := []string{"🇩🇪 Germany 🟢 23%", "🛜 ∞ 🛜", "🇳🇱 Netherlands 🟡 64%"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("remarks = %q, want %q", got, want)
	}
}

func TestSubscriptionLoadSuffixReachesEveryFormat(t *testing.T) {
	fx := newLoadFixture(t)
	vm := lhost("🇩🇪 Germany", "10.0.0.1", 20001, 1)
	vm["sni"], vm["path"], vm["security"] = "example.com", "/ws", "tls"
	fx.putHostsByTag(map[string][]map[string]interface{}{
		"main": {lhost("🇩🇪 Germany", "10.0.0.1", 20001, 0)},
		"vm":   {vm},
		"ss":   {lhost("🇩🇪 Germany", "10.0.0.1", 20001, 2)},
	})
	fx.setPresence(true, map[string]map[string]interface{}{"10.0.0.1": {"20001": 910}})
	sub := fx.createUser("load_formats_user", "vless", "vmess", "shadowsocks")

	// Every format shares the one remark engine, so each carries the suffix.
	for _, format := range []string{"sing-box", "clash", "clash-meta", "outline", "v2ray-json"} {
		resp := doRequest(t, fx.router, "GET", "/sub/"+sub+"/"+format, "", nil)
		if resp.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", format, resp.Code, resp.Raw)
		}
		if !strings.Contains(string(resp.Raw), "🇩🇪 Germany 🔴 91%") {
			t.Errorf("%s config is missing the load suffix: %s", format, resp.Raw)
		}
	}
}

func TestSubscriptionWithoutPresenceIsUnchanged(t *testing.T) {
	fx := newLoadFixture(t)
	fx.putHosts(
		lhost("🇩🇪 Germany", "10.0.0.1", 20001, 0),
		lhost("Explicit {LOAD}", "10.0.0.1", 20002, 1),
		lhost("🇳🇱 Netherlands", "10.0.0.2", 20001, 2),
	)
	// Nothing was written to Redis: presence:live is absent.
	fx.h.WithConfigLoad(true, 1000, true) // even with sorting on
	sub := fx.createUser("load_dark_user")

	got := fx.linkRemarks(sub)
	want := []string{"🇩🇪 Germany", "Explicit", "🇳🇱 Netherlands"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("remarks = %q, want the originals in priority order %q", got, want)
	}
}

func TestSubscriptionIndicatorOffKeepsPlainRemarks(t *testing.T) {
	fx := newLoadFixture(t)
	fx.putHosts(
		lhost("🇩🇪 Germany", "10.0.0.1", 20001, 0),
		lhost("🇳🇱 Netherlands {LOAD_PERCENT}", "10.0.0.2", 20001, 1),
	)
	fx.h.WithConfigLoad(false, 1000, false)
	fx.setPresence(true, map[string]map[string]interface{}{
		"10.0.0.1": {"20001": 230}, "10.0.0.2": {"20001": 640},
	})
	sub := fx.createUser("load_off_user")

	got := fx.linkRemarks(sub)
	want := []string{"🇩🇪 Germany", "🇳🇱 Netherlands 64%"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("remarks = %q, want %q", got, want)
	}
}

func TestSubscriptionSortByLoadOrdersLeastLoadedFirst(t *testing.T) {
	fx := newLoadFixture(t)
	fx.putHosts(
		lhost("A busy", "10.0.0.1", 20001, 0),
		lhost("🛜 {DATA_LEFT} 🛜", "10.0.0.1", 20001, 1),
		lhost("B free", "10.0.0.1", 20002, 2),
		lhost("C free too", "10.0.0.1", 20003, 3),
		lhost("D idle", "10.0.0.1", 20004, 4),
	)
	fx.h.WithConfigLoad(true, 1000, true)
	fx.setPresence(true, map[string]map[string]interface{}{
		"10.0.0.1": {"20001": 800, "20002": 100, "20003": 100},
	})
	sub := fx.createUser("load_sort_user")

	got := fx.linkRemarks(sub)
	// D idle (0%) < B (10%) = C (10%, tie keeps priority order) < A (80%); the
	// info host keeps its slot at index 1.
	want := []string{"D idle 🟢 0%", "🛜 ∞ 🛜", "B free 🟢 10%", "C free too 🟢 10%", "A busy 🟠 80%"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("remarks = %q, want %q", got, want)
	}

	// The same data with sorting off keeps the admin's priority order.
	fx.h.WithConfigLoad(true, 1000, false)
	got = fx.linkRemarks(sub)
	want = []string{"A busy 🟠 80%", "🛜 ∞ 🛜", "B free 🟢 10%", "C free too 🟢 10%", "D idle 🟢 0%"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("unsorted remarks = %q, want %q", got, want)
	}
}

func TestSubscriptionLoadUsesConfiguredCapacity(t *testing.T) {
	fx := newLoadFixture(t)
	fx.putHosts(lhost("🇩🇪 Germany", "10.0.0.1", 20001, 0))
	fx.h.WithConfigLoad(true, 200, false)
	fx.setPresence(true, map[string]map[string]interface{}{"10.0.0.1": {"20001": 100}})
	sub := fx.createUser("load_capacity_user")

	if got := fx.linkRemarks(sub); len(got) != 1 || got[0] != "🇩🇪 Germany 🟡 50%" {
		t.Fatalf("remarks = %q, want 100/200 = 50%%", got)
	}
}

func TestGetHostsLoadShape(t *testing.T) {
	fx := newLoadFixture(t)
	fx.putHosts(
		lhost("🇩🇪 Germany", "10.0.0.1", 20001, 0),
		lhost("🛜 {DATA_LEFT} 🛜", "10.0.0.1", 20001, 1),
		lhost("🇳🇱 Netherlands", "10.0.0.2", 20001, 2),
	)
	fx.setPresence(true, map[string]map[string]interface{}{
		"10.0.0.1": {"20001": 312},
		"10.0.0.2": {"20001": 905},
	})

	resp := doRequest(t, fx.router, "GET", "/api/hosts/load", fx.token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/hosts/load = %d %s", resp.Code, resp.Raw)
	}
	var body struct {
		Capacity   int    `json:"capacity"`
		Indicator  bool   `json:"indicator"`
		SortByLoad bool   `json:"sort_by_load"`
		UpdatedAt  string `json:"updated_at"`
		Hosts      []struct {
			HostID  int32   `json:"host_id"`
			Remark  string  `json:"remark"`
			Address string  `json:"address"`
			Port    *int    `json:"port"`
			Conns   *int    `json:"conns"`
			Percent *int    `json:"percent"`
			Level   string  `json:"level"`
			NodeIDs []int32 `json:"node_ids"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatalf("decode: %v: %s", err, resp.Raw)
	}
	if body.Capacity != 1000 || !body.Indicator || body.SortByLoad {
		t.Errorf("settings = capacity %d indicator %v sort %v, want 1000 / true / false", body.Capacity, body.Indicator, body.SortByLoad)
	}
	if _, err := time.Parse(time.RFC3339, body.UpdatedAt); err != nil {
		t.Errorf("updated_at = %q is not RFC3339: %v", body.UpdatedAt, err)
	}
	if len(body.Hosts) != 2 {
		t.Fatalf("hosts = %d, want 2 (the info host is not listed): %s", len(body.Hosts), resp.Raw)
	}

	de, nl := body.Hosts[0], body.Hosts[1]
	if de.Remark != "🇩🇪 Germany" || de.Address != "10.0.0.1" || de.Port == nil || *de.Port != 20001 {
		t.Errorf("first host = %+v", de)
	}
	if de.Conns == nil || *de.Conns != 312 || de.Percent == nil || *de.Percent != 31 || de.Level != "free" {
		t.Errorf("germany load = conns %v percent %v level %s, want 312 / 31 / free", de.Conns, de.Percent, de.Level)
	}
	if len(de.NodeIDs) != 1 || de.NodeIDs[0] != fx.nodes["10.0.0.1"] {
		t.Errorf("germany node_ids = %v, want [%d]", de.NodeIDs, fx.nodes["10.0.0.1"])
	}
	if nl.Percent == nil || *nl.Percent != 91 || nl.Level != "full" || len(nl.NodeIDs) != 1 || nl.NodeIDs[0] != fx.nodes["10.0.0.2"] {
		t.Errorf("netherlands = %+v", nl)
	}
}

func TestGetHostsLoadUnknownWithoutPresence(t *testing.T) {
	fx := newLoadFixture(t)
	fx.putHosts(lhost("🇩🇪 Germany", "10.0.0.1", 20001, 0))

	resp := doRequest(t, fx.router, "GET", "/api/hosts/load", fx.token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/hosts/load = %d %s", resp.Code, resp.Raw)
	}
	hosts, _ := resp.Body["hosts"].([]interface{})
	if len(hosts) != 1 {
		t.Fatalf("hosts = %v", resp.Body["hosts"])
	}
	host := hosts[0].(map[string]interface{})
	for _, key := range []string{"conns", "percent"} {
		v, present := host[key]
		if !present || v != nil {
			t.Errorf("%s = %v (present=%v), want an explicit null", key, v, present)
		}
	}
	if host["level"] != "unknown" {
		t.Errorf("level = %v, want unknown", host["level"])
	}
	if ids, ok := host["node_ids"].([]interface{}); !ok || len(ids) != 0 {
		t.Errorf("node_ids = %v, want an empty array", host["node_ids"])
	}
}

func TestGetHostsLoadEmptyPanel(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "GET", "/api/hosts/load", token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/hosts/load = %d %s", resp.Code, resp.Raw)
	}
	if hosts, ok := resp.Body["hosts"].([]interface{}); !ok || len(hosts) != 0 {
		t.Errorf("hosts = %v, want an empty array (not null)", resp.Body["hosts"])
	}
}

func TestGetHostsLoadRequiresSudo(t *testing.T) {
	router, token := newTestRouter(t)
	if resp := doRequest(t, router, "GET", "/api/hosts/load", "", nil); resp.Code != http.StatusUnauthorized {
		t.Errorf("without a token = %d, want 401", resp.Code)
	}

	// An ordinary (non-sudo) admin must be refused too.
	doRequest(t, router, "POST", "/api/admin", token, map[string]interface{}{
		"username": "load-reseller", "password": "pw12345", "is_sudo": false,
	})
	resellerToken := loginAs(t, router, "load-reseller", "pw12345")
	if resp := doRequest(t, router, "GET", "/api/hosts/load", resellerToken, nil); resp.Code != http.StatusForbidden {
		t.Errorf("as a non-sudo admin = %d, want 403", resp.Code)
	}
}
