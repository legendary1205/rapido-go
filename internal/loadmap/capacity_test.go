package loadmap

import "testing"

// Tests of the per-node capacity rule: the home nodes of a port, the node's own
// load as a floor for every config on it, and each node's capacity against the
// panel default. The fakes come from loadmap_test.go.

func newMapCap(inv Inventory, pres Presence, capacity int) *Map {
	return New(Config{Capacity: capacity, Resolver: newResolver(nil)}, inv, pres)
}

// proxiedHost is a host whose address matches no node (a proxied name, the
// usual case), so every node serving its port is a candidate.
func proxiedHost(port int) Host {
	return Host{ID: 7, Remark: "Cfg", Address: "203.0.113.9", Port: port, InboundTag: "main"}
}

func TestHomeNodesAreTheOnesCarryingAtLeast20PercentOfThePort(t *testing.T) {
	// Three nodes serve port 443. A and B carry the traffic; C is a stray: its 10
	// connections are 0.2% of the port's 5000, and its other port makes it a fully
	// loaded node that must NOT drag the config to 100%.
	inv := &fakeInventory{
		nodes: []Node{
			{ID: 1, Name: "a", Address: "10.0.0.1", Status: "connected"},                 // default capacity
			{ID: 2, Name: "b", Address: "10.0.0.2", Status: "connected", Capacity: 2000}, // its own
			{ID: 3, Name: "c", Address: "10.0.0.3", Status: "connected", Capacity: 5000},
		},
		hosts: []Host{proxiedHost(443)},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reportingTotal(3990, map[int]int{443: 3990}),
		2: reportingTotal(1000, map[int]int{443: 1000}), // exactly 20% of 5000: home
		3: reportingTotal(5000, map[int]int{443: 10, 8443: 4990}),
	})}
	hl := build(newMapCap(inv, pres, 10000)).For(7)
	if hl == nil || !hl.Known {
		t.Fatalf("host should be known, got %+v", hl)
	}
	if hl.Conns != 5000 {
		t.Errorf("conns = %d, want 5000 (everyone on the config, home node or not)", hl.Conns)
	}
	if len(hl.NodeIDs) != 2 || hl.NodeIDs[0] != 1 || hl.NodeIDs[1] != 2 {
		t.Errorf("home nodes = %v, want [1 2]", hl.NodeIDs)
	}
	// Node 1: 3990/10000 = 40%. Node 2: 1000/2000 = 50%. The stray node's 100% stays out.
	if hl.PortPercent != 50 || hl.NodePercent != 50 || hl.Percent != 50 || hl.Level != LevelNormal {
		t.Errorf("port %d node %d percent %d level %s, want 50 / 50 / 50 / normal", hl.PortPercent, hl.NodePercent, hl.Percent, hl.Level)
	}

	// One connection more elsewhere pushes node 2 just under the 20% line: it
	// stops being a home node and the config reads node 1 alone.
	pres.set(live(map[int32]NodePresence{
		1: reportingTotal(3990, map[int]int{443: 3990}),
		2: reportingTotal(1000, map[int]int{443: 1000}),
		3: reportingTotal(5000, map[int]int{443: 11, 8443: 4989}),
	}))
	hl = build(newMapCap(inv, pres, 10000)).For(7)
	if hl.Conns != 5001 || len(hl.NodeIDs) != 1 || hl.NodeIDs[0] != 1 || hl.Percent != 40 {
		t.Errorf("just below 20%%: conns %d home %v percent %d, want 5001 / [1] / 40", hl.Conns, hl.NodeIDs, hl.Percent)
	}
}

func TestAnIdlePortMakesEveryCandidateAHomeNode(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{
			{ID: 1, Address: "10.0.0.1", Status: "connected"},
			{ID: 2, Address: "10.0.0.2", Status: "connected"},
		},
		hosts: []Host{proxiedHost(443)},
	}
	// Nobody is on port 443, but the nodes are busy with other ports: the
	// emptier config still reads as busy as the busier node.
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reportingTotal(2000, map[int]int{8443: 2000}),
		2: reportingTotal(7000, map[int]int{8443: 7000}),
	})}
	hl := build(newMapCap(inv, pres, 10000)).For(7)
	if hl == nil || !hl.Known || hl.Conns != 0 || hl.PortPercent != 0 {
		t.Fatalf("idle port = %+v, want known with 0 connections", hl)
	}
	if len(hl.NodeIDs) != 2 || hl.NodePercent != 70 || hl.Percent != 70 || hl.Level != LevelBusy {
		t.Errorf("home %v node %d percent %d level %s, want both nodes / 70 / 70 / busy", hl.NodeIDs, hl.NodePercent, hl.Percent, hl.Level)
	}
}

func TestNodeLoadDominatesAQuietConfig(t *testing.T) {
	// The config on 20001 has 30 people, but its node carries 8500 on another
	// config: everyone on a node shares its CPU, so the quiet config is not free.
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}},
		hosts: []Host{
			{ID: 7, Remark: "Quiet", Address: "10.0.0.1", Port: 20001, InboundTag: "main"},
			{ID: 8, Remark: "Busy", Address: "10.0.0.1", Port: 20002, InboundTag: "main"},
		},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reportingTotal(8530, map[int]int{20001: 30, 20002: 8500}),
	})}
	s := build(newMapCap(inv, pres, 10000))

	quiet := s.For(7)
	if quiet.Conns != 30 || quiet.PortPercent != 0 || quiet.NodePercent != 85 || quiet.Percent != 85 || quiet.Level != LevelBusy {
		t.Errorf("quiet config = conns %d port %d node %d percent %d level %s, want 30 / 0 / 85 / 85 / busy",
			quiet.Conns, quiet.PortPercent, quiet.NodePercent, quiet.Percent, quiet.Level)
	}
	busy := s.For(8)
	if busy.Conns != 8500 || busy.PortPercent != 85 || busy.NodePercent != 85 || busy.Percent != 85 {
		t.Errorf("busy config = %+v, want 8500 people, 85 / 85 / 85", busy)
	}
}

func TestTheNodeTotalIsNeverBelowItsPorts(t *testing.T) {
	// A source that leaves Total unset (or reports less than the ports add up
	// to) still gives a node total of at least the sum of its ports.
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Address: "10.0.0.1", Status: "connected"}},
		hosts: []Host{{ID: 7, Remark: "Cfg", Address: "10.0.0.1", Port: 20001, InboundTag: "main"}},
	}
	for name, np := range map[string]NodePresence{
		"total unset":     reporting(map[int]int{20001: 400, 20002: 300}),
		"total too small": reportingTotal(100, map[int]int{20001: 400, 20002: 300}),
	} {
		s := build(newMapCap(inv, &fakePresence{data: live(map[int32]NodePresence{1: np})}, 1000))
		hl := s.For(7)
		if hl.PortPercent != 40 || hl.NodePercent != 70 || hl.Percent != 70 {
			t.Errorf("%s: port %d node %d percent %d, want 40 / 70 / 70", name, hl.PortPercent, hl.NodePercent, hl.Percent)
		}
		if len(s.Nodes) != 1 || s.Nodes[0].Conns != 700 {
			t.Errorf("%s: node load = %+v, want 700 connections", name, s.Nodes)
		}
	}
}

func TestPerNodeCapacityAgainstTheDefault(t *testing.T) {
	// Same 3000 connections on both nodes: the one with its own 15000 capacity
	// is at 20%, the one without falls back to the default 10000 and is at 30%.
	inv := &fakeInventory{
		nodes: []Node{
			{ID: 1, Name: "big", Address: "10.0.0.1", Status: "connected", Capacity: 15000},
			{ID: 2, Name: "plain", Address: "10.0.0.2", Status: "connected"}, // capacity NULL
		},
		hosts: []Host{
			{ID: 7, Remark: "OnBig", Address: "10.0.0.1", Port: 20001, InboundTag: "main"},
			{ID: 8, Remark: "OnPlain", Address: "10.0.0.2", Port: 20001, InboundTag: "main"},
		},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reportingTotal(3000, map[int]int{20001: 3000}),
		2: reportingTotal(3000, map[int]int{20001: 3000}),
	})}
	s := build(newMapCap(inv, pres, 10000))

	if hl := s.For(7); hl.Percent != 20 || hl.Level != LevelFree {
		t.Errorf("host on the node with its own capacity: percent %d level %s, want 20 / free", hl.Percent, hl.Level)
	}
	if hl := s.For(8); hl.Percent != 30 || hl.Level != LevelFree {
		t.Errorf("host on the node with a NULL capacity: percent %d level %s, want 30 (the default) / free", hl.Percent, hl.Level)
	}
	want := []NodeLoad{
		{ID: 1, Name: "big", Conns: 3000, Capacity: 15000, CapacitySource: CapacityNode, Percent: 20},
		{ID: 2, Name: "plain", Conns: 3000, Capacity: 10000, CapacitySource: CapacityDefault, Percent: 30},
	}
	if len(s.Nodes) != 2 || s.Nodes[0] != want[0] || s.Nodes[1] != want[1] {
		t.Errorf("nodes = %+v, want %+v", s.Nodes, want)
	}
	if s.Capacity != 10000 {
		t.Errorf("snapshot default capacity = %d, want 10000", s.Capacity)
	}
}

func TestNodeThatIsNotReportingIsLeftOutEverywhere(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{
			{ID: 1, Name: "up", Address: "10.0.0.1", Status: "connected"},
			{ID: 2, Name: "silent", Address: "10.0.0.2", Status: "connected", Capacity: 100}, // would read 100% if it counted
			{ID: 3, Name: "gone", Address: "10.0.0.3", Status: "connected"},
			{ID: 4, Name: "off", Address: "10.0.0.4", Status: "disabled"},
		},
		hosts: []Host{proxiedHost(443)},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reportingTotal(500, map[int]int{443: 500}),
		2: {Reporting: false, Total: 9999, Ports: map[int]int{443: 9999}}, // stale keys, not reporting
		// node 3 has no entry at all
		4: reportingTotal(9999, map[int]int{443: 9999}), // disabled by the admin
	})}
	s := build(newMapCap(inv, pres, 1000))

	hl := s.For(7)
	if hl.Conns != 500 || len(hl.NodeIDs) != 1 || hl.NodeIDs[0] != 1 || hl.Percent != 50 {
		t.Errorf("host = conns %d nodes %v percent %d, want only node 1: 500 / [1] / 50", hl.Conns, hl.NodeIDs, hl.Percent)
	}
	if len(s.Nodes) != 1 || s.Nodes[0].ID != 1 || s.Nodes[0].Name != "up" {
		t.Errorf("node list = %+v, want just the reporting node", s.Nodes)
	}

	// Nothing reporting at all (presence:live gone): no node list either.
	dark := build(newMapCap(inv, &fakePresence{data: PresenceData{Live: false}}, 1000))
	if len(dark.Nodes) != 0 || dark.For(7).Known {
		t.Errorf("without presence: nodes %+v known %v, want none and unknown", dark.Nodes, dark.For(7).Known)
	}
}

func TestPercentsClampAt100(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{{ID: 1, Name: "hot", Address: "10.0.0.1", Status: "connected", Capacity: 1000}},
		hosts: []Host{{ID: 7, Remark: "Cfg", Address: "10.0.0.1", Port: 443, InboundTag: "main"}},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		1: reportingTotal(2500, map[int]int{443: 1800, 8443: 700}),
	})}
	s := build(newMapCap(inv, pres, 10000))
	hl := s.For(7)
	if hl.PortPercent != 100 || hl.NodePercent != 100 || hl.Percent != 100 || hl.Level != LevelFull {
		t.Errorf("over capacity: port %d node %d percent %d level %s, want 100 / 100 / 100 / full", hl.PortPercent, hl.NodePercent, hl.Percent, hl.Level)
	}
	if hl.Conns != 1800 {
		t.Errorf("conns = %d, want the real 1800 (only percents are clamped)", hl.Conns)
	}
	if len(s.Nodes) != 1 || s.Nodes[0].Percent != 100 || s.Nodes[0].Conns != 2500 {
		t.Errorf("node = %+v, want 2500 connections reading 100%%", s.Nodes)
	}
}

func TestNodeLoadsListOnlyEnabledReportingNodesByID(t *testing.T) {
	inv := &fakeInventory{
		nodes: []Node{
			{ID: 9, Name: "nine", Address: "10.0.0.9", Status: "connected"},
			{ID: 2, Name: "two", Address: "10.0.0.2", Status: "connecting"},
		},
	}
	pres := &fakePresence{data: live(map[int32]NodePresence{
		9: reportingTotal(0, nil), // reporting, nobody connected
		2: reportingTotal(120, map[int]int{443: 120}),
	})}
	s := build(newMapCap(inv, pres, 1000))
	if len(s.Nodes) != 2 || s.Nodes[0].ID != 2 || s.Nodes[1].ID != 9 {
		t.Fatalf("nodes = %+v, want ids [2 9]", s.Nodes)
	}
	if s.Nodes[0].Percent != 12 || s.Nodes[1].Conns != 0 || s.Nodes[1].Percent != 0 {
		t.Errorf("nodes = %+v, want 12%% and an idle 0", s.Nodes)
	}
}
