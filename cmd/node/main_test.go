package main

import (
	"context"
	"testing"

	"github.com/legendary1205/rapido-go/internal/nodecore"
	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

func vlessInbound(tag string, users ...userSpec) inboundSpec {
	return inboundSpec{Tag: tag, Protocol: "vless", ListenPort: 8443, Users: users}
}

func TestDiffPulledConfigFirstPullAlwaysRestarts(t *testing.T) {
	needsRestart, tags := diffPulledConfig(nil, pulledConfig{Inbounds: []inboundSpec{vlessInbound("in1")}})
	if !needsRestart || tags != nil {
		t.Errorf("first pull: needsRestart=%v tags=%v, want true/nil", needsRestart, tags)
	}
}

func TestDiffPulledConfigIdenticalIsNoOp(t *testing.T) {
	cfg := pulledConfig{Version: "v1", Inbounds: []inboundSpec{vlessInbound("in1", userSpec{Name: "a", UUID: "u1"})}}
	needsRestart, tags := diffPulledConfig(&cfg, cfg)
	if needsRestart || len(tags) != 0 {
		t.Errorf("identical config: needsRestart=%v tags=%v, want false/empty", needsRestart, tags)
	}
}

func TestDiffPulledConfigOnlyVlessUsersChangedHotApplies(t *testing.T) {
	old := pulledConfig{Inbounds: []inboundSpec{vlessInbound("in1", userSpec{Name: "a", UUID: "u1"})}}
	next := pulledConfig{Inbounds: []inboundSpec{vlessInbound("in1", userSpec{Name: "a", UUID: "u1"}, userSpec{Name: "b", UUID: "u2"})}}
	needsRestart, tags := diffPulledConfig(&old, next)
	if needsRestart {
		t.Fatal("only a vless inbound's users changed - want a hot apply, not a restart")
	}
	if len(tags) != 1 || tags[0] != "in1" {
		t.Errorf("tags = %v, want [in1]", tags)
	}
}

func TestDiffPulledConfigNonVlessUsersChangedNeedsRestart(t *testing.T) {
	old := pulledConfig{Inbounds: []inboundSpec{{Tag: "in1", Protocol: "trojan", ListenPort: 443, Users: []userSpec{{Name: "a", Password: "p1"}}}}}
	next := pulledConfig{Inbounds: []inboundSpec{{Tag: "in1", Protocol: "trojan", ListenPort: 443, Users: []userSpec{{Name: "a", Password: "p2"}}}}}
	needsRestart, _ := diffPulledConfig(&old, next)
	if !needsRestart {
		t.Error("a non-vless inbound's user list changed - there is no hot-update path for it, want a restart")
	}
}

func TestDiffPulledConfigInboundShapeChangedNeedsRestart(t *testing.T) {
	old := pulledConfig{Inbounds: []inboundSpec{vlessInbound("in1")}}
	next := pulledConfig{Inbounds: []inboundSpec{{Tag: "in1", Protocol: "vless", ListenPort: 9443}}} // port changed
	needsRestart, _ := diffPulledConfig(&old, next)
	if !needsRestart {
		t.Error("listen port changed - want a restart, hot-apply can't rebind a listener")
	}
}

func TestDiffPulledConfigInboundAddedOrRemovedNeedsRestart(t *testing.T) {
	old := pulledConfig{Inbounds: []inboundSpec{vlessInbound("in1")}}
	next := pulledConfig{Inbounds: []inboundSpec{vlessInbound("in1"), vlessInbound("in2")}}
	if needsRestart, _ := diffPulledConfig(&old, next); !needsRestart {
		t.Error("a new inbound appeared - want a restart")
	}
	if needsRestart, _ := diffPulledConfig(&next, old); !needsRestart {
		t.Error("an inbound disappeared - want a restart")
	}
}

func TestDiffPulledConfigCoreChangeNeedsRestart(t *testing.T) {
	old := pulledConfig{Inbounds: []inboundSpec{vlessInbound("in1")}, Core: coreSpec{LogLevel: "warn"}}
	next := pulledConfig{Inbounds: []inboundSpec{vlessInbound("in1")}, Core: coreSpec{LogLevel: "debug"}}
	if needsRestart, _ := diffPulledConfig(&old, next); !needsRestart {
		t.Error("core config changed (log level) - want a restart even though no inbound changed at all")
	}
}

// TestBuildOptionsWithFullCoreIsAcceptedBySingBox is the real proof that
// buildCoreOptions's hand-built sing-box option structs are actually
// valid, not just "compiles" - it builds a realistic Core Config (a
// custom outbound, a routing rule targeting it, a DNS server, sniffing
// enabled) end to end through nodecore.New/Start, the exact same path a
// real node process uses, and asserts sing-box's own real startup
// validation accepts it without error.
func TestBuildOptionsWithFullCoreIsAcceptedBySingBox(t *testing.T) {
	req := startRequest{
		Inbounds: []inboundSpec{{Tag: "vless-in", Protocol: "vless", ListenPort: 0, Users: []userSpec{{Name: "u", UUID: "8f8a4c1e-1e2a-4b8a-9b1a-000000000099"}}}},
		Core: &coreSpec{
			LogLevel:     "warn",
			SniffEnabled: true,
			Outbounds: []outboundSpec{
				{Tag: "upstream", Type: "socks", Server: "127.0.0.1", ServerPort: 1080},
			},
			RoutingRules: []routingRuleSpec{
				{IPIsPrivate: true, OutboundTag: "block"},
				{DomainSuffix: []string{"example.com"}, OutboundTag: "upstream"},
			},
			DNSServers: []dnsServerSpec{
				{Tag: "dns1", Type: "udp", Address: "1.1.1.1"},
			},
		},
	}
	opts, err := buildOptions(req)
	if err != nil {
		t.Fatalf("buildOptions: %v", err)
	}
	if len(opts.Outbounds) != 3 { // direct-out, block-out, upstream
		t.Fatalf("len(Outbounds) = %d, want 3 (implicit direct+block, plus the custom one)", len(opts.Outbounds))
	}
	if opts.Route == nil || len(opts.Route.Rules) != 3 { // sniff-all + 2 routing rules
		t.Fatalf("Route.Rules = %v, want 3 (sniff + 2 custom rules)", opts.Route)
	}
	if opts.DNS == nil || len(opts.DNS.Servers) != 1 {
		t.Fatalf("DNS.Servers = %v, want 1", opts.DNS)
	}

	node, err := nodecore.New(context.Background(), opts, traffic.NewManager())
	if err != nil {
		t.Fatalf("nodecore.New rejected the built options: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("sing-box rejected the built options at Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })
}

func TestBuildOptionsRejectsUnknownOutboundType(t *testing.T) {
	req := startRequest{Core: &coreSpec{Outbounds: []outboundSpec{{Tag: "x", Type: "not-a-real-type"}}}}
	if _, err := buildOptions(req); err == nil {
		t.Error("buildOptions accepted an unknown outbound type, want an error")
	}
}

func TestBuildOptionsNilCoreMatchesOriginalHardcodedBehavior(t *testing.T) {
	opts, err := buildOptions(startRequest{Inbounds: []inboundSpec{vlessInbound("in1")}})
	if err != nil {
		t.Fatalf("buildOptions: %v", err)
	}
	if len(opts.Outbounds) != 2 {
		t.Errorf("len(Outbounds) = %d, want 2 (just the implicit direct+block, no Core supplied)", len(opts.Outbounds))
	}
	if opts.Route == nil || opts.Route.Final != implicitOutboundTagDirect {
		t.Errorf("Route.Final = %v, want %q", opts.Route, implicitOutboundTagDirect)
	}
}
