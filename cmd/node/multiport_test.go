package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	sbox "github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/legendary1205/rapido-go/internal/nodecore"
	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

func startEcho(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer ln.Close()
	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

// echoVia connects to a node listener as a real VLESS client and asks it to
// proxy to the echo server; nil means the payload came back intact.
func echoVia(nodePort uint16, uuid, echoHost string, echoPort int) error {
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(nodePort))), 2*time.Second)
	if err != nil {
		return err
	}
	defer raw.Close()
	client, err := vless.NewClient(uuid, "", logger.NOP())
	if err != nil {
		return err
	}
	conn, err := client.DialConn(raw, M.Socksaddr{Addr: netip.MustParseAddr(echoHost), Port: uint16(echoPort)})
	if err != nil {
		return err
	}
	const payload = "ping-through-the-node"
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	buf := make([]byte, len(payload))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != payload {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func startPlanned(t *testing.T, req startRequest) (*nodecore.Node, nodePlan) {
	t.Helper()
	opts, plan, err := buildOptionsPlan(req)
	if err != nil {
		t.Fatalf("buildOptionsPlan: %v", err)
	}
	node, err := nodecore.New(context.Background(), opts, traffic.NewManager())
	if err != nil {
		t.Fatalf("nodecore.New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })
	return node, plan
}

const (
	testUserA = "8f8a4c1e-1e2a-4b8a-9b1a-0000000000a1"
	testUserB = "8f8a4c1e-1e2a-4b8a-9b1a-0000000000b2"
)

func TestBuildOptionsExpandsAMultiPortInboundIntoOneListenerPerPort(t *testing.T) {
	req := startRequest{Inbounds: []inboundSpec{
		{Tag: "main", Protocol: "vless", ListenPort: 20000, ListenPorts: []uint16{20000, 20004, 20008},
			Users: []userSpec{{Name: "a", UUID: testUserA}}},
		{Tag: "solo", Protocol: "vless", ListenPort: 30000, Users: []userSpec{{Name: "a", UUID: testUserA}}},
	}}
	opts, plan, err := buildOptionsPlan(req)
	if err != nil {
		t.Fatal(err)
	}

	var tags []string
	ports := map[string]uint16{}
	for _, in := range opts.Inbounds {
		tags = append(tags, in.Tag)
		ports[in.Tag] = in.Options.(*sbox.VLESSInboundOptions).ListenPort
	}
	want := []string{"main#20000", "main#20004", "main#20008", "solo"}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("listener tags = %v, want %v", tags, want)
	}
	if ports["main#20004"] != 20004 || ports["solo"] != 30000 {
		t.Errorf("listener ports = %v", ports)
	}
	if got := plan.listenerTags("main"); !reflect.DeepEqual(got, want[:3]) {
		t.Errorf("listenerTags(main) = %v, want the three derived tags", got)
	}
	if got := plan.listenerTags("solo"); !reflect.DeepEqual(got, []string{"solo"}) {
		t.Errorf("listenerTags(solo) = %v, a single-port inbound keeps its own tag", got)
	}
}

func TestInboundMatchTags(t *testing.T) {
	portsByTag := map[string][]uint16{
		"main": {20000, 20004, 20008},
		"solo": {30000},
	}
	cases := []struct {
		name    string
		inbound []string
		ports   []int
		want    []string
	}{
		{"whole multi-port inbound", []string{"main"}, nil, []string{"main#20000", "main#20004", "main#20008"}},
		{"one port of it", []string{"main"}, []int{20004}, []string{"main#20004"}},
		{"two ports, one of them not served", []string{"main"}, []int{20008, 1}, []string{"main#20008"}},
		{"single-port inbound, port matches", []string{"solo"}, []int{30000}, []string{"solo"}},
		{"single-port inbound, other port", []string{"solo"}, []int{1}, nil},
		{"tag this config does not define is kept and simply matches nothing", []string{"ghost"}, []int{20000}, []string{"ghost"}},
		{"several tags", []string{"solo", "main"}, []int{30000, 20000}, []string{"solo", "main#20000"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inboundMatchTags(tc.inbound, tc.ports, portsByTag); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("inboundMatchTags = %v, want %v", got, tc.want)
			}
		})
	}
}

// A rule whose port restriction excludes every listener must vanish. Emitted
// without an inbound criterion it would match every connection on every
// listener and quietly send all traffic to its outbound.
func TestRuleWhosePortsMatchNothingIsDroppedNotWidened(t *testing.T) {
	req := startRequest{
		Inbounds: []inboundSpec{{Tag: "main", Protocol: "vless", ListenPort: 20000, ListenPorts: []uint16{20000, 20004}}},
		Core: &coreSpec{RoutingRules: []routingRuleSpec{
			{Inbound: []string{"main"}, InboundPort: []int{9}, OutboundTag: "block"},
			{Inbound: []string{"main"}, InboundPort: []int{20004}, OutboundTag: "direct"},
		}},
	}
	opts, _, err := buildOptionsPlan(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Route.Rules) != 1 {
		t.Fatalf("rules = %d, want 1 (the unmatched-port rule dropped)", len(opts.Route.Rules))
	}
	got := []string(opts.Route.Rules[0].DefaultOptions.Inbound)
	if !reflect.DeepEqual(got, []string{"main#20004"}) {
		t.Errorf("surviving rule matches %v, want only main#20004", got)
	}
}

func TestDirectFallbackBuildsAPrimaryAFallbackAndASelector(t *testing.T) {
	req := startRequest{Core: &coreSpec{
		Outbounds: []outboundSpec{
			{Tag: "uk", Type: "direct", BindInterface: "uk", DirectFallback: true},
			{Tag: "plain", Type: "direct", BindInterface: "eth9"},
			{Tag: "de-ir", Type: "direct", DirectFallback: true},
		},
		RoutingRules: []routingRuleSpec{{OutboundTag: "uk"}},
	}}
	opts, plan, err := buildOptionsPlan(req)
	if err != nil {
		t.Fatal(err)
	}

	type ob struct {
		typ  string
		bind string
	}
	got := map[string]ob{}
	for _, o := range opts.Outbounds {
		b := ""
		if d, ok := o.Options.(*sbox.DirectOutboundOptions); ok {
			b = d.BindInterface
		}
		got[o.Tag] = ob{o.Type, b}
	}
	for tag, want := range map[string]ob{
		"uk~wg":     {"direct", "uk"},
		"uk~direct": {"direct", ""},
		"uk":        {"selector", ""},
		"plain":     {"direct", "eth9"},
		"de-ir":     {"direct", ""},
	} {
		if got[tag] != want {
			t.Errorf("outbound %q = %+v, want %+v", tag, got[tag], want)
		}
	}
	if _, ok := got["plain~wg"]; ok {
		t.Error("an outbound without direct_fallback must stay a single plain outbound")
	}
	if _, ok := got["de-ir~wg"]; ok {
		t.Error("direct_fallback with no bind_interface has nothing to fall back from")
	}

	want := []nodecore.FallbackGroup{{Group: "uk", Primary: "uk~wg", Fallback: "uk~direct", Interface: "uk"}}
	if !reflect.DeepEqual(plan.fallbacks, want) {
		t.Errorf("fallbacks = %+v, want %+v", plan.fallbacks, want)
	}
	if got := plan.watchedInterfaces(); !reflect.DeepEqual(got, []string{"uk"}) {
		t.Errorf("watchedInterfaces = %v", got)
	}
}

func TestDiffPulledConfigPortListChangeNeedsRestart(t *testing.T) {
	old := pulledConfig{Version: "v1", Inbounds: []inboundSpec{
		{Tag: "main", Protocol: "vless", ListenPort: 20000, ListenPorts: []uint16{20000, 20004}},
	}}
	next := pulledConfig{Version: "v2", Inbounds: []inboundSpec{
		{Tag: "main", Protocol: "vless", ListenPort: 20000, ListenPorts: []uint16{20000, 20004, 20008}},
	}}
	if restart, _ := diffPulledConfig(&old, next); !restart {
		t.Error("a port added to an inbound changes its shape: that is a restart, not a hot user update")
	}
}

// The heart of "one inbound with routing": one logical inbound listens on two
// ports and the rules send each port to a different exit. Real sockets, a real
// sing-box, a real VLESS client.
func TestOneInboundRoutesEachPortToItsOwnExit(t *testing.T) {
	echoHost, echoPort := startEcho(t)
	p1, p2 := freeTCPPort(t), freeTCPPort(t)

	_, plan := startPlanned(t, startRequest{
		Inbounds: []inboundSpec{{
			Tag: "main", Protocol: "vless", ListenPort: p1, ListenPorts: []uint16{p1, p2},
			Users: []userSpec{{Name: "a", UUID: testUserA}},
		}},
		Core: &coreSpec{RoutingRules: []routingRuleSpec{
			{Inbound: []string{"main"}, InboundPort: []int{int(p1)}, OutboundTag: "direct"},
			{Inbound: []string{"main"}, InboundPort: []int{int(p2)}, OutboundTag: "block"},
		}},
	})
	_ = plan

	if err := echoVia(p1, testUserA, echoHost, echoPort); err != nil {
		t.Errorf("port %d is routed to the direct exit but the round trip failed: %v", p1, err)
	}
	if err := echoVia(p2, testUserA, echoHost, echoPort); err == nil {
		t.Errorf("port %d is routed to the block exit but traffic went through", p2)
	}
}

func TestUsersAreHotUpdatedOnEveryListenerOfAMultiPortInbound(t *testing.T) {
	echoHost, echoPort := startEcho(t)
	p1, p2 := freeTCPPort(t), freeTCPPort(t)

	node, plan := startPlanned(t, startRequest{
		Inbounds: []inboundSpec{{
			Tag: "main", Protocol: "vless", ListenPort: p1, ListenPorts: []uint16{p1, p2},
			Users: []userSpec{{Name: "a", UUID: testUserA}},
		}},
	})

	for _, p := range []uint16{p1, p2} {
		if err := echoVia(p, testUserB, echoHost, echoPort); err == nil {
			t.Fatalf("user B is not registered yet but got through on port %d", p)
		}
	}

	users := []sbox.VLESSUser{{Name: "a", UUID: testUserA}, {Name: "b", UUID: testUserB}}
	for _, listener := range plan.listenerTags("main") {
		if err := node.UpdateVLESSUsers(listener, users); err != nil {
			t.Fatalf("UpdateVLESSUsers(%s): %v", listener, err)
		}
	}
	for _, p := range []uint16{p1, p2} {
		if err := echoVia(p, testUserB, echoHost, echoPort); err != nil {
			t.Errorf("user B, hot-added, cannot use port %d: %v", p, err)
		}
	}
}

// fakeHealth lets a test flip a tunnel between up and down without touching
// a real network interface.
type fakeHealth struct {
	mu   sync.Mutex
	down map[string]bool
}

func (f *fakeHealth) Up(iface string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.down[iface]
}

func (f *fakeHealth) setDown(iface string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down == nil {
		f.down = map[string]bool{}
	}
	f.down[iface] = down
}

// The whole point of the fallback: an exit pinned to an interface that is
// gone still carries traffic once the supervisor sees the tunnel is down, and
// goes back to the tunnel when it recovers.
func TestExitFallsBackToDirectWhileItsTunnelIsDownAndReturnsWhenItRecovers(t *testing.T) {
	echoHost, echoPort := startEcho(t)
	p1 := freeTCPPort(t)
	const iface = "rapido-test-gone0"

	node, plan := startPlanned(t, startRequest{
		Inbounds: []inboundSpec{{
			Tag: "main", Protocol: "vless", ListenPort: p1,
			Users: []userSpec{{Name: "a", UUID: testUserA}},
		}},
		Core: &coreSpec{
			Outbounds:    []outboundSpec{{Tag: "exit", Type: "direct", BindInterface: iface, DirectFallback: true}},
			RoutingRules: []routingRuleSpec{{Inbound: []string{"main"}, OutboundTag: "exit"}},
		},
	})
	if len(plan.fallbacks) != 1 {
		t.Fatalf("fallbacks = %+v, want 1", plan.fallbacks)
	}
	health := &fakeHealth{}
	sup := nodecore.NewSupervisor(node, plan.fallbacks, health, slog.New(slog.NewTextHandler(io.Discard, nil)))

	health.setDown(iface, true)
	sup.Reconcile()
	if got, _ := node.SelectedOutbound("exit"); got != "exit~direct" {
		t.Fatalf("selected = %q with the tunnel down, want exit~direct", got)
	}
	if !sup.ActiveByInterface()[iface] {
		t.Error("ActiveByInterface must report the tunnel as being on fallback")
	}
	if err := echoVia(p1, testUserA, echoHost, echoPort); err != nil {
		t.Fatalf("with the tunnel down the exit must still work over the fallback: %v", err)
	}

	health.setDown(iface, false)
	sup.Reconcile()
	if got, _ := node.SelectedOutbound("exit"); got != "exit~wg" {
		t.Fatalf("selected = %q after the tunnel recovered, want exit~wg", got)
	}
	if sup.ActiveByInterface()[iface] {
		t.Error("the tunnel is healthy again, fallback must no longer be reported")
	}
	if runtime.GOOS == "linux" {
		if err := echoVia(p1, testUserA, echoHost, echoPort); err == nil {
			t.Error("back on the tunnel, an exit pinned to a nonexistent interface should fail: proof the primary really is the bound one")
		}
	}
}
