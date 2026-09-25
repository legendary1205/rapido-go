package loadmap

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- fakes -----------------------------------------------------------------

type fakeInventory struct {
	nodes    []Node
	hosts    []Host
	nodesErr error
	hostsErr error
	calls    atomic.Int32
	gate     chan struct{} // when set, Hosts blocks until it is closed
}

func (f *fakeInventory) Nodes(context.Context) ([]Node, error) { return f.nodes, f.nodesErr }
func (f *fakeInventory) Hosts(context.Context) ([]Host, error) {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	return f.hosts, f.hostsErr
}

type fakePresence struct {
	mu    sync.Mutex
	data  PresenceData
	err   error
	calls atomic.Int32
}

func (f *fakePresence) Read(_ context.Context, ids []int32) (PresenceData, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data, f.err
}

func (f *fakePresence) set(d PresenceData) {
	f.mu.Lock()
	f.data = d
	f.mu.Unlock()
}

type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][]string
	errs    map[string]error
	calls   map[string]int
	gate    chan struct{}
}

func newResolver(answers map[string][]string) *fakeResolver {
	return &fakeResolver{answers: answers, errs: map[string]error{}, calls: map[string]int{}}
}

func (r *fakeResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[host]++
	if err := r.errs[host]; err != nil {
		return nil, err
	}
	if a, ok := r.answers[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func (r *fakeResolver) callCount(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[host]
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// idle waits until no rebuild and no DNS lookup is running.
func idle(m *Map) {
	for {
		m.mu.Lock()
		f := m.flight
		m.mu.Unlock()
		if f == nil {
			break
		}
		<-f.done
	}
	m.dns.wg.Wait()
}

func live(nodes map[int32]NodePresence) PresenceData {
	return PresenceData{Live: true, Nodes: nodes}
}

func reporting(ports map[int]int) NodePresence { return NodePresence{Reporting: true, Ports: ports} }

// build returns a snapshot built right now, bypassing the cache.
func build(m *Map) *Snapshot {
	s := m.build(context.Background())
	m.dns.wg.Wait()
	return s
}

// --- percent / level -------------------------------------------------------

func TestLevelBoundaries(t *testing.T) {
	cases := []struct {
		percent int
		want    Level
		emoji   string
	}{
		{0, LevelFree, "🟢"},
		{39, LevelFree, "🟢"},
		{40, LevelNormal, "🟡"},
		{69, LevelNormal, "🟡"},
		{70, LevelBusy, "🟠"},
		{89, LevelBusy, "🟠"},
		{90, LevelFull, "🔴"},
		{100, LevelFull, "🔴"},
	}
	for _, c := range cases {
		got := LevelFor(c.percent)
		if got != c.want {
			t.Errorf("LevelFor(%d) = %q, want %q", c.percent, got, c.want)
		}
		if got.Emoji() != c.emoji {
			t.Errorf("Emoji(%q) = %q, want %q", got, got.Emoji(), c.emoji)
		}
	}
	if LevelUnknown.Emoji() != "" {
		t.Errorf("unknown level must have no emoji, got %q", LevelUnknown.Emoji())
	}
}

func TestPercentRoundsAndClamps(t *testing.T) {
	cases := []struct {
		conns    float64
		capacity int
		want     int
	}{
		{0, 1000, 0},
		{230, 1000, 23},
		{394, 1000, 39},
		{395, 1000, 40}, // 39.5 rounds up onto the next level's boundary
		{2, 3, 67},
		{1500, 1000, 100}, // over capacity clamps to 100
		{-5, 1000, 0},
		{500, 0, 50},  // a bad capacity falls back to the default 1000
		{500, -3, 50}, //
		{1, 3, 33},
		{1000, 1000, 100},
		{12.5, 25, 50}, // a fractional count still rounds to a percent
	}
	for _, c := range cases {
		if got := Percent(c.conns, c.capacity); got != c.want {
			t.Errorf("Percent(%v, %d) = %d, want %d", c.conns, c.capacity, got, c.want)
		}
	}
}

func TestIsInfoAndUsesLoadVariable(t *testing.T) {
	cases := []struct {
		remark   string
		info     bool
		usesLoad bool
	}{
		{"🇩🇪 Germany", false, false},
		{"", false, false},
		{"🛜 {DATA_LEFT} 🛜", true, false},
		{"{USERNAME} - Node", true, false},
		{"🇩🇪 Germany {LOAD}", false, true},
		{"{LOAD_EMOJI} Germany", false, true},
		{"Germany {LOAD_PERCENT} {DATA_LEFT}", false, true},
	}
	for _, c := range cases {
		if got := IsInfo(c.remark); got != c.info {
			t.Errorf("IsInfo(%q) = %v, want %v", c.remark, got, c.info)
		}
		if got := UsesLoadVariable(c.remark); got != c.usesLoad {
			t.Errorf("UsesLoadVariable(%q) = %v, want %v", c.remark, got, c.usesLoad)
		}
	}
}

// --- host -> node mapping --------------------------------------------------

func newMap(inv Inventory, pres Presence, res Resolver, clock *fakeClock) *Map {
	cfg := Config{Capacity: 1000, Resolver: res}
	if clock != nil {
		cfg.Now = clock.Now
	}
	return New(cfg, inv, pres)
}

func TestNodeMatchedByIPUsesOnlyThatNode(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}, {ID: 2, Address: "10.0.0.2", Status: "connected"}},
		hosts: []Host{{ID: 7, Remark: "🇩🇪 Germany", Address: "10.0.0.2", Port: 20001, InboundTag: "main"}},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reporting(map[int]int{20001: 900}),
		2: reporting(map[int]int{20001: 230}),
	})}
	s := build(newMap(inv, pres, newResolver(nil), nil))

	hl := s.For(7)
	if hl == nil || !hl.Known {
		t.Fatalf("host 7 should be known, got %+v", hl)
	}
	if hl.Conns != 230 || hl.Percent != 23 || hl.Level != LevelFree {
		t.Errorf("load = conns %d percent %d level %s, want 230 / 23 / free", hl.Conns, hl.Percent, hl.Level)
	}
	if len(hl.NodeIDs) != 1 || hl.NodeIDs[0] != 2 {
		t.Errorf("node ids = %v, want [2]", hl.NodeIDs)
	}
}

func TestSeveralMatchingNodesUseTheSum(t *testing.T) {
	// One name resolving to both nodes' IPs: the load is what both carry together.
	res := newResolver(map[string][]string{"lb.example.test": {"10.0.0.1", "10.0.0.2"}})
	inv := &fakeInventory{
		nodes: []Node{
			{ID: 1, Address: "10.0.0.1", Status: "connected"},
			{ID: 2, Address: "10.0.0.2", Status: "connected"},
			{ID: 3, Address: "10.0.0.3", Status: "connected"},
		},
		hosts: []Host{{ID: 7, Remark: "Pool", Address: "lb.example.test", Port: 443, InboundTag: "main"}},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reporting(map[int]int{443: 100}),
		2: reporting(map[int]int{443: 301}),
		3: reporting(map[int]int{443: 999}), // not behind the name: must not count
	})}
	clock := newClock()
	m := newMap(inv, pres, res, clock)

	first := build(m) // first build only starts the lookup: name unresolved yet
	if hl := first.For(7); hl == nil || len(hl.NodeIDs) != 3 {
		t.Fatalf("before the name resolves every reporting node is used, got %+v", hl)
	}
	second := build(m)
	hl := second.For(7)
	if hl == nil || !hl.Known {
		t.Fatalf("host 7 should be known, got %+v", hl)
	}
	if len(hl.NodeIDs) != 2 || hl.NodeIDs[0] != 1 || hl.NodeIDs[1] != 2 {
		t.Fatalf("node ids = %v, want [1 2]", hl.NodeIDs)
	}
	// 100 + 301 = 401 of the default capacity 1000 -> 40%
	if hl.Percent != 40 || hl.Conns != 401 {
		t.Errorf("summed load = conns %d percent %d, want 401 / 40", hl.Conns, hl.Percent)
	}
}

func TestNoAddressMatchFallsBackToEveryNodeServingThePort(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}, {ID: 2, Address: "10.0.0.2", Status: "connected"}},
		// 203.0.113.9 is a proxy in front of the nodes, not any node's address.
		hosts: []Host{{ID: 7, Remark: "Proxied", Address: "203.0.113.9", Port: 20001, InboundTag: "main"}},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reporting(map[int]int{20001: 100}),
		2: reporting(map[int]int{20001: 300}),
	})}
	hl := build(newMap(inv, pres, newResolver(nil), nil)).For(7)
	if hl == nil || !hl.Known || hl.Conns != 400 || hl.Percent != 40 {
		t.Fatalf("want the sum 400 / 40%% over both nodes, got %+v", hl)
	}
	if len(hl.NodeIDs) != 2 {
		t.Errorf("node ids = %v, want both nodes", hl.NodeIDs)
	}
}

func TestIdlePortOnAServingNodeReadsZeroNotUnknown(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}},
		hosts: []Host{{ID: 7, Remark: "Quiet", Address: "10.0.0.1", Port: 20005, InboundTag: "main"}},
	}
	// The node reports (total key exists) but has connections on another port only.
	pres := &fakePresence{data: live(map[int32]NodePresence{1: reporting(map[int]int{20001: 50})})}
	hl := build(newMap(inv, pres, newResolver(nil), nil)).For(7)
	if hl == nil || !hl.Known || hl.Conns != 0 || hl.Percent != 0 || hl.Level != LevelFree {
		t.Fatalf("an idle port on a reporting node should read 0%% free, got %+v", hl)
	}
}

func TestNodeProfileFiltersWhichNodeServesAHost(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{
			{ID: 1, Address: "10.0.0.1", Status: "connected", InboundTags: []string{"other"}},
			{ID: 2, Address: "10.0.0.2", Status: "connected", ListenPorts: []int32{20009}},
			{ID: 3, Address: "10.0.0.3", Status: "connected"},
		},
		hosts: []Host{{ID: 7, Remark: "Cfg", Address: "203.0.113.9", Port: 20001, InboundTag: "main"}},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reporting(nil), 2: reporting(nil), 3: reporting(map[int]int{20001: 400}),
	})}
	hl := build(newMap(inv, pres, newResolver(nil), nil)).For(7)
	if hl == nil || len(hl.NodeIDs) != 1 || hl.NodeIDs[0] != 3 || hl.Percent != 40 || hl.Level != LevelNormal {
		t.Fatalf("only node 3 serves main:20001, got %+v", hl)
	}
}

func TestUnknownWhenNothingReports(t *testing.T) {
	host := Host{ID: 7, Remark: "Cfg", Address: "10.0.0.1", Port: 20001, InboundTag: "main"}
	nodes := []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}}

	cases := map[string]struct {
		nodes []Node
		pres  *fakePresence
	}{
		"presence:live absent": {nodes, &fakePresence{data: PresenceData{Live: false, Nodes: map[int32]NodePresence{1: reporting(map[int]int{20001: 5})}}}},
		"node not reporting":   {nodes, &fakePresence{data: live(map[int32]NodePresence{1: {Reporting: false}})}},
		"node missing":         {nodes, &fakePresence{data: live(map[int32]NodePresence{})}},
		"redis error":          {nodes, &fakePresence{err: errors.New("connection refused")}},
		"node disabled":        {[]Node{{ID: 1, Address: "10.0.0.1", Status: "disabled"}}, &fakePresence{data: live(map[int32]NodePresence{1: reporting(map[int]int{20001: 5})})}},
		"no nodes at all":      {nil, &fakePresence{data: live(nil)}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			inv := &fakeInventory{nodes: c.nodes, hosts: []Host{host}}
			s := build(newMap(inv, c.pres, newResolver(nil), nil))
			hl := s.For(7)
			if hl == nil {
				t.Fatal("the host must still be listed, just unknown")
			}
			if hl.Known || hl.Level != LevelUnknown || len(hl.NodeIDs) != 0 {
				t.Errorf("want unknown, got %+v", hl)
			}
			if s.HasData() {
				t.Error("HasData must be false")
			}
		})
	}
}

func TestInventoryErrorYieldsAnEmptySnapshot(t *testing.T) {
	inv := &fakeInventory{hostsErr: errors.New("db down")}
	s := build(newMap(inv, &fakePresence{}, newResolver(nil), nil))
	if s == nil || len(s.Hosts) != 0 || s.For(1) != nil || s.HasData() {
		t.Fatalf("want an empty snapshot, got %+v", s)
	}

	// Nodes failing alone still lists the hosts, all unknown.
	inv = &fakeInventory{nodesErr: errors.New("db down"), hosts: []Host{{ID: 1, Remark: "A", Address: "1.1.1.1", Port: 443}}}
	s = build(newMap(inv, &fakePresence{}, newResolver(nil), nil))
	if hl := s.For(1); hl == nil || hl.Known {
		t.Fatalf("host should be listed as unknown, got %+v", hl)
	}
}

func TestInfoHostsAndPortlessHostsAreHandled(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}},
		hosts: []Host{
			{ID: 1, Remark: "🛜 {DATA_LEFT} 🛜", Address: "10.0.0.1", Port: 20001},
			{ID: 2, Remark: "Real", Address: "10.0.0.1", Port: 20001},
			{ID: 3, Remark: "Explicit {LOAD}", Address: "10.0.0.1", Port: 20001},
			{ID: 4, Remark: "No port", Address: "10.0.0.1", Port: 0},
		},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{1: reporting(map[int]int{20001: 100})})}
	s := build(newMap(inv, pres, newResolver(nil), nil))

	if s.For(1) != nil {
		t.Error("an info host must not be part of the snapshot")
	}
	if hl := s.For(2); hl == nil || !hl.Known || hl.UsesLoad {
		t.Errorf("host 2 = %+v, want known and !UsesLoad", hl)
	}
	if hl := s.For(3); hl == nil || !hl.Known || !hl.UsesLoad {
		t.Errorf("host 3 = %+v, want known and UsesLoad", hl)
	}
	if hl := s.For(4); hl == nil || hl.Known {
		t.Errorf("host 4 has no port, want listed but unknown, got %+v", hl)
	}
	if len(s.Hosts) != 3 || s.Hosts[0].HostID != 2 || s.Hosts[1].HostID != 3 || s.Hosts[2].HostID != 4 {
		t.Errorf("hosts must keep inventory order without the info host, got %+v", s.Hosts)
	}
}

func TestNilSnapshotIsSafe(t *testing.T) {
	var s *Snapshot
	if s.For(1) != nil || s.HasData() {
		t.Error("a nil snapshot knows nothing")
	}
}

func TestAddressesWithVariablesAreNeverResolved(t *testing.T) {
	res := newResolver(map[string][]string{"{SERVER_IP}": {"10.0.0.1"}})
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}, {ID: 2, Address: "10.0.0.2", Status: "connected"}},
		hosts: []Host{
			{ID: 7, Remark: "Cfg", Address: "{USERNAME}.example.test", Port: 20001},
			{ID: 8, Remark: "Cfg2", Address: "{SERVER_IP}", Port: 20001},
		},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reporting(map[int]int{20001: 100}), 2: reporting(map[int]int{20001: 300}),
	})}
	m := New(Config{Resolver: res, ExpandAddress: func(a string) string {
		if a == "{SERVER_IP}" {
			return "10.0.0.2"
		}
		return a
	}}, inv, pres)
	build(m)
	s := build(m)
	if n := res.callCount("{USERNAME}.example.test") + res.callCount("{SERVER_IP}"); n != 0 {
		t.Errorf("a name holding a {variable} must never reach the resolver, got %d lookups", n)
	}
	if hl := s.For(7); len(hl.NodeIDs) != 2 {
		t.Errorf("unresolvable address -> every serving node, got %v", hl.NodeIDs)
	}
	// {SERVER_IP} expanded to node 2's IP before matching.
	if hl := s.For(8); len(hl.NodeIDs) != 1 || hl.NodeIDs[0] != 2 {
		t.Errorf("expanded {SERVER_IP} should match node 2 only, got %v", hl.NodeIDs)
	}
}

// --- DNS -------------------------------------------------------------------

func TestDNSNeverBlocksTheBuildAndFillsInAfterwards(t *testing.T) {
	res := newResolver(map[string][]string{"video.example.test": {"10.0.0.2"}})
	res.gate = make(chan struct{}) // the resolver hangs until released
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}, {ID: 2, Address: "10.0.0.2", Status: "connected"}},
		hosts: []Host{{ID: 7, Remark: "Cfg", Address: "video.example.test", Port: 20001}},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reporting(map[int]int{20001: 900}), 2: reporting(map[int]int{20001: 100}),
	})}
	m := newMap(inv, pres, res, nil)

	done := make(chan *Snapshot, 1)
	go func() { done <- m.build(context.Background()) }()
	var s *Snapshot
	select {
	case s = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("build blocked on DNS")
	}
	if hl := s.For(7); hl == nil || len(hl.NodeIDs) != 2 || hl.Conns != 1000 {
		t.Fatalf("first miss must fall back to every serving node, got %+v", hl)
	}

	close(res.gate)
	m.dns.wg.Wait()
	s = m.build(context.Background())
	if hl := s.For(7); hl == nil || len(hl.NodeIDs) != 1 || hl.NodeIDs[0] != 2 || hl.Conns != 100 {
		t.Fatalf("once resolved the host maps to node 2 only, got %+v", hl)
	}
	if n := res.callCount("video.example.test"); n != 1 {
		t.Errorf("lookups = %d, want 1 (cached)", n)
	}
}

func TestDNSCacheExpiryRefreshesInBackgroundKeepingTheStaleAnswer(t *testing.T) {
	clock := newClock()
	res := newResolver(map[string][]string{"a.example.test": {"10.0.0.1"}})
	c := newDNSCache(res, clock.Now)

	if got := c.lookup("a.example.test"); got != nil {
		t.Fatalf("first miss must return nothing, got %v", got)
	}
	c.wg.Wait()
	if _, ok := c.lookup("a.example.test")["10.0.0.1"]; !ok {
		t.Fatal("the cached answer is missing")
	}
	clock.Advance(4*time.Minute + 59*time.Second)
	c.lookup("a.example.test")
	c.wg.Wait()
	if n := res.callCount("a.example.test"); n != 1 {
		t.Fatalf("lookups before the 5 min TTL = %d, want 1", n)
	}

	// Past the TTL: the old answer is still returned while one refresh runs,
	// and the resolver now failing does not wipe it.
	clock.Advance(2 * time.Second)
	res.mu.Lock()
	res.errs["a.example.test"] = errors.New("SERVFAIL")
	res.mu.Unlock()
	if _, ok := c.lookup("a.example.test")["10.0.0.1"]; !ok {
		t.Fatal("a stale entry must still be served during the refresh")
	}
	c.wg.Wait()
	if n := res.callCount("a.example.test"); n != 2 {
		t.Fatalf("lookups after expiry = %d, want 2", n)
	}
	if _, ok := c.lookup("a.example.test")["10.0.0.1"]; !ok {
		t.Fatal("a failed refresh must keep the previous answer")
	}
	// A failed entry is retried after a minute, not on every call.
	c.lookup("a.example.test")
	c.wg.Wait()
	if n := res.callCount("a.example.test"); n != 2 {
		t.Errorf("failed entry retried too early: %d lookups", n)
	}
	clock.Advance(61 * time.Second)
	c.lookup("a.example.test")
	c.wg.Wait()
	if n := res.callCount("a.example.test"); n != 3 {
		t.Errorf("failed entry not retried after the fail TTL: %d lookups", n)
	}
}

func TestDNSIPLiteralsNeedNoLookupAndAreNormalized(t *testing.T) {
	res := newResolver(nil)
	c := newDNSCache(res, time.Now)
	if _, ok := c.lookup("10.0.0.1")["10.0.0.1"]; !ok {
		t.Error("IPv4 literal must answer itself")
	}
	if _, ok := c.lookup("::ffff:10.0.0.9")["10.0.0.9"]; !ok {
		t.Error("an IPv4-mapped IPv6 literal must normalize to IPv4")
	}
	if _, ok := c.lookup("2001:0db8:0000:0000:0000:0000:0000:0001")["2001:db8::1"]; !ok {
		t.Error("IPv6 literal must normalize to its canonical form")
	}
	c.wg.Wait()
	if len(res.calls) != 0 {
		t.Errorf("IP literals must not hit the resolver, got %v", res.calls)
	}
}

func TestDNSResolvedIPv6AnswersMatchNodeAddresses(t *testing.T) {
	res := newResolver(map[string][]string{"v6.example.test": {"2001:db8::7", "::ffff:10.0.0.4"}})
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "2001:0db8::7", Status: "connected"}, {ID: 2, Address: "10.0.0.9", Status: "connected"}},
		hosts: []Host{{ID: 7, Remark: "Cfg", Address: "v6.example.test", Port: 443}},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{1: reporting(map[int]int{443: 10}), 2: reporting(map[int]int{443: 500})})}
	m := newMap(inv, pres, res, nil)
	build(m)
	hl := build(m).For(7)
	if hl == nil || len(hl.NodeIDs) != 1 || hl.NodeIDs[0] != 1 {
		t.Fatalf("v6 name should match node 1 by canonical IP, got %+v", hl)
	}
}

// --- cache / single flight -------------------------------------------------

func hostAndPresence() (*fakeInventory, *fakePresence) {
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}},
		hosts: []Host{{ID: 7, Remark: "Cfg", Address: "10.0.0.1", Port: 20001, InboundTag: "main"}},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{1: reporting(map[int]int{20001: 230})})}
	return inv, pres
}

func TestSnapshotIsCachedForThreeSeconds(t *testing.T) {
	inv, pres := hostAndPresence()
	clock := newClock()
	m := newMap(inv, pres, newResolver(nil), clock)
	ctx := context.Background()

	s1 := m.Snapshot(ctx)
	if hl := s1.For(7); hl == nil || hl.Percent != 23 {
		t.Fatalf("first snapshot = %+v", hl)
	}
	clock.Advance(2900 * time.Millisecond)
	if s2 := m.Snapshot(ctx); s2 != s1 {
		t.Error("a snapshot younger than 3 s must be returned as is")
	}
	if n := pres.calls.Load(); n != 1 {
		t.Fatalf("redis reads within the TTL = %d, want 1", n)
	}

	// Presence changes; past the TTL the old snapshot is still handed out
	// immediately while one background rebuild picks the change up.
	pres.set(live(map[int32]NodePresence{1: reporting(map[int]int{20001: 730})}))
	clock.Advance(200 * time.Millisecond)
	if s3 := m.Snapshot(ctx); s3 != s1 {
		t.Error("a stale-but-recent snapshot must still be served without waiting")
	}
	idle(m)
	if n := pres.calls.Load(); n != 2 {
		t.Fatalf("redis reads after the TTL = %d, want 2", n)
	}
	if hl := m.Snapshot(ctx).For(7); hl == nil || hl.Percent != 73 || hl.Level != LevelBusy {
		t.Fatalf("refreshed snapshot = %+v, want 73%% busy", hl)
	}
}

func TestVeryStaleSnapshotIsRebuiltBeforeUse(t *testing.T) {
	inv, pres := hostAndPresence()
	clock := newClock()
	m := newMap(inv, pres, newResolver(nil), clock)
	ctx := context.Background()
	m.Snapshot(ctx)

	pres.set(live(map[int32]NodePresence{1: reporting(map[int]int{20001: 990})}))
	clock.Advance(time.Minute) // no subscription traffic for a while
	hl := m.Snapshot(ctx).For(7)
	if hl == nil || hl.Percent != 99 || hl.Level != LevelFull {
		t.Fatalf("a minute-old snapshot must not be served, got %+v", hl)
	}
}

func TestConcurrentCallersShareOneRefresh(t *testing.T) {
	inv, pres := hostAndPresence()
	inv.gate = make(chan struct{})
	m := newMap(inv, pres, newResolver(nil), newClock())

	const callers = 64
	results := make(chan *Snapshot, callers)
	var started sync.WaitGroup
	started.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			started.Done()
			results <- m.Snapshot(context.Background())
		}()
	}
	started.Wait()
	// Every caller is now parked on the one build that is stuck on the gate.
	time.Sleep(50 * time.Millisecond)
	close(inv.gate)

	var first *Snapshot
	for i := 0; i < callers; i++ {
		select {
		case s := <-results:
			if first == nil {
				first = s
			} else if s != first {
				t.Fatal("callers received different snapshots from one refresh")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a caller never returned")
		}
	}
	if n := inv.calls.Load(); n != 1 {
		t.Errorf("inventory reads = %d, want exactly 1 for %d concurrent callers", n, callers)
	}
	if n := pres.calls.Load(); n != 1 {
		t.Errorf("redis reads = %d, want exactly 1", n)
	}
}

func TestColdCallerStopsWaitingWhenItsContextEnds(t *testing.T) {
	inv, pres := hostAndPresence()
	inv.gate = make(chan struct{}) // the build never finishes during this test
	m := newMap(inv, pres, newResolver(nil), newClock())
	defer close(inv.gate)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	s := m.Snapshot(ctx)
	if time.Since(start) > time.Second {
		t.Fatal("Snapshot did not honor its context")
	}
	if s == nil || s.HasData() || s.For(7) != nil {
		t.Errorf("want an empty (never nil) snapshot, got %+v", s)
	}
}

type panickyInventory struct{ fakeInventory }

func (p *panickyInventory) Hosts(context.Context) ([]Host, error) { panic("boom") }

func TestPanickingSourceCannotWedgeTheMap(t *testing.T) {
	inv := &panickyInventory{}
	m := newMap(inv, &fakePresence{}, newResolver(nil), newClock())
	s := m.Snapshot(context.Background())
	if s == nil || s.HasData() {
		t.Fatalf("want an empty snapshot, got %+v", s)
	}
	idle(m) // would hang forever if the flight were never released
}

func TestCapacityAppliesToPercent(t *testing.T) {
	inv, pres := hostAndPresence()
	m := New(Config{Capacity: 500, Resolver: newResolver(nil)}, inv, pres)
	if m.Capacity() != 500 {
		t.Errorf("capacity = %d, want 500", m.Capacity())
	}
	hl := build(m).For(7)
	if hl == nil || hl.Percent != 46 { // 230/500
		t.Fatalf("percent = %+v, want 46", hl)
	}
	if New(Config{Capacity: 0}, inv, pres).Capacity() != DefaultCapacity {
		t.Error("a zero capacity must fall back to the default")
	}
}
