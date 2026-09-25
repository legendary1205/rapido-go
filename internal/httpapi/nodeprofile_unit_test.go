package httpapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }

func fleetCoreForMerge() coreConfigDTO {
	return coreConfigDTO{
		LogLevel: "warn", SniffEnabled: true,
		Outbounds: []outboundDTO{
			{Tag: "exit-a", Type: "socks", Server: "1.1.1.1", ServerPort: 1080},
			{Tag: "exit-c", Type: "direct"},
		},
		RoutingRules: []routingRuleDTO{{Inbound: []string{"alpha"}, OutboundTag: "exit-a"}},
		DNSServers:   []dnsServerDTO{{Tag: "d1", Type: "udp", Address: "8.8.8.8"}},
	}
}

func TestCoreOverridesMergeOutboundsByTagAndPrependRules(t *testing.T) {
	fleet := fleetCoreForMerge()
	dns := []dnsServerDTO{{Tag: "d2", Type: "tls", Address: "1.1.1.1"}}
	ov := coreOverridesDTO{
		LogLevel: stringPtr("debug"), SniffEnabled: boolPtr(false), DNSServers: &dns,
		Outbounds: []outboundDTO{
			{Tag: "exit-a", Type: "socks", Server: "2.2.2.2", ServerPort: 1081}, // replaces in place
			{Tag: "exit-b", Type: "direct"},                                     // appended
		},
		RoutingRulesFirst: []routingRuleDTO{{Inbound: []string{"multi"}, InboundPort: []int{20001}, OutboundTag: "exit-b"}},
	}
	got := ov.apply(fleet)

	if got.LogLevel != "debug" || got.SniffEnabled {
		t.Errorf("log_level/sniff = %q/%v, want debug/false", got.LogLevel, got.SniffEnabled)
	}
	if len(got.DNSServers) != 1 || got.DNSServers[0].Tag != "d2" {
		t.Errorf("dns = %v, want only d2 (replaces the fleet list)", got.DNSServers)
	}
	tags := make([]string, len(got.Outbounds))
	for i, ob := range got.Outbounds {
		tags[i] = ob.Tag
	}
	if !reflect.DeepEqual(tags, []string{"exit-a", "exit-c", "exit-b"}) {
		t.Errorf("outbound order = %v, want [exit-a exit-c exit-b] (replace in place, append new)", tags)
	}
	if got.Outbounds[0].Server != "2.2.2.2" {
		t.Errorf("exit-a server = %q, want the override's 2.2.2.2", got.Outbounds[0].Server)
	}
	if len(got.RoutingRules) != 2 || got.RoutingRules[0].OutboundTag != "exit-b" || got.RoutingRules[1].OutboundTag != "exit-a" {
		t.Errorf("rules = %+v, want the override rule first, then the fleet rule", got.RoutingRules)
	}

	// The fleet value every node shares must be untouched.
	if fleet.Outbounds[0].Server != "1.1.1.1" || len(fleet.RoutingRules) != 1 || fleet.DNSServers[0].Tag != "d1" || fleet.LogLevel != "warn" {
		t.Errorf("apply modified the fleet config: %+v", fleet)
	}
}

func TestCoreOverridesEmptyDNSListReplacesFleetList(t *testing.T) {
	empty := []dnsServerDTO{}
	got := coreOverridesDTO{DNSServers: &empty}.apply(fleetCoreForMerge())
	if got.DNSServers == nil || len(got.DNSServers) != 0 {
		t.Errorf("dns = %#v, want an explicit empty list", got.DNSServers)
	}
	if !(coreOverridesDTO{}).isZero() || (coreOverridesDTO{DNSServers: &empty}).isZero() {
		t.Error("an absent dns_servers key is a no-op, an explicit empty one is not")
	}
}

func TestParseCoreOverridesIsStrict(t *testing.T) {
	for _, raw := range []string{``, `null`, `{}`, `  {} `} {
		ov, err := parseCoreOverrides([]byte(raw))
		if err != nil || !ov.isZero() {
			t.Errorf("parse(%q) = %+v, %v; want zero value and no error", raw, ov, err)
		}
	}
	cases := map[string]string{
		`{"outbounds_typo": []}`:             "unknown field",
		`[]`:                                 "JSON object",
		`"x"`:                                "JSON object",
		`{"log_level": 5}`:                   "core_overrides",
		`{"outbounds": [{"tag": 7}]}`:        "core_overrides",
		`{"log_level": "debug"} {"a": 1}`:    "after the JSON object",
		`{"dns_servers": {"tag": "nope"}}`:   "core_overrides",
		`{"routing_rules_first": "nothing"}`: "core_overrides",
	}
	for raw, want := range cases {
		if _, err := parseCoreOverrides([]byte(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parse(%s) error = %v, want it to mention %q", raw, err, want)
		}
	}
}

func TestValidateCoreOverridesChecksTheMergedConfig(t *testing.T) {
	fleet := fleetCoreForMerge()
	known := map[string]bool{"alpha": true, "multi": true}
	cases := []struct {
		name string
		ov   coreOverridesDTO
		want string // substring of the message; empty = valid
	}{
		{"empty", coreOverridesDTO{}, ""},
		{"rule to a fleet outbound", coreOverridesDTO{RoutingRulesFirst: []routingRuleDTO{{OutboundTag: "exit-a"}}}, ""},
		{"rule to a builtin", coreOverridesDTO{RoutingRulesFirst: []routingRuleDTO{{OutboundTag: "block"}}}, ""},
		{"rule to an outbound the override adds", coreOverridesDTO{
			Outbounds:         []outboundDTO{{Tag: "new", Type: "direct"}},
			RoutingRulesFirst: []routingRuleDTO{{OutboundTag: "new"}},
		}, ""},
		{"rule to nothing", coreOverridesDTO{RoutingRulesFirst: []routingRuleDTO{{OutboundTag: "nope"}}}, "unknown outbound: nope"},
		{"reserved tag", coreOverridesDTO{Outbounds: []outboundDTO{{Tag: "direct", Type: "direct"}}}, "reserved"},
		{"bad outbound type", coreOverridesDTO{Outbounds: []outboundDTO{{Tag: "x", Type: "wat"}}}, "invalid outbound type"},
		{"socks without server", coreOverridesDTO{Outbounds: []outboundDTO{{Tag: "x", Type: "socks"}}}, "server and server_port"},
		{"duplicate in the override", coreOverridesDTO{Outbounds: []outboundDTO{{Tag: "x", Type: "direct"}, {Tag: "x", Type: "direct"}}}, "duplicate outbound tag: x"},
		{"selector member unknown", coreOverridesDTO{Outbounds: []outboundDTO{{Tag: "s", Type: "selector", Outbounds: []string{"ghost"}}}}, "unknown member outbound ghost"},
		{"bad log level", coreOverridesDTO{LogLevel: stringPtr("loud")}, "invalid log_level"},
		{"unknown inbound in a rule", coreOverridesDTO{RoutingRulesFirst: []routingRuleDTO{{Inbound: []string{"ghost"}, OutboundTag: "block"}}}, "unknown inbound: ghost"},
		{"inbound_port without inbound", coreOverridesDTO{RoutingRulesFirst: []routingRuleDTO{{InboundPort: []int{1}, OutboundTag: "block"}}}, "inbound_port requires"},
		{"bad dns type", coreOverridesDTO{DNSServers: &[]dnsServerDTO{{Tag: "d", Type: "carrier-pigeon"}}}, "invalid dns server type"},
	}
	for _, tc := range cases {
		got := validateCoreOverrides(fleet, tc.ov, known)
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: unexpected message %q", tc.name, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) || !strings.HasPrefix(got, "core_overrides: ") {
			t.Errorf("%s: message %q, want core_overrides: ...%s...", tc.name, got, tc.want)
		}
	}
	// knownInbounds == nil skips only the inbound-tag check.
	rule := coreOverridesDTO{RoutingRulesFirst: []routingRuleDTO{{Inbound: []string{"ghost"}, OutboundTag: "block"}}}
	if msg := validateCoreOverrides(fleet, rule, nil); msg != "" {
		t.Errorf("nil known set should skip the inbound check, got %q", msg)
	}
}

func specFor(tag string, port uint16, ports ...uint16) nodeConfigInboundSpec {
	return nodeConfigInboundSpec{Tag: tag, Protocol: "vless", ListenPort: port, ListenPorts: ports, Users: []nodeConfigUserSpec{}}
}

func TestFilterInboundsByTagAndPort(t *testing.T) {
	fleet := []nodeConfigInboundSpec{
		specFor("alpha", 8443),
		specFor("beta", 2087),
		specFor("multi", 20002, 20000, 20001, 20002),
	}
	tags := func(in []nodeConfigInboundSpec) []string {
		out := []string{}
		for _, s := range in {
			out = append(out, s.Tag)
		}
		return out
	}

	// Default: the fleet slice itself.
	if got := (nodeProfile{}).filterInbounds(fleet); &got[0] != &fleet[0] {
		t.Error("the default profile must be handed the fleet slice untouched")
	}

	got := nodeProfile{InboundTags: []string{"alpha", "multi", "gone"}}.filterInbounds(fleet)
	if !reflect.DeepEqual(tags(got), []string{"alpha", "multi"}) {
		t.Errorf("tags filter = %v, want [alpha multi]", tags(got))
	}
	if !reflect.DeepEqual(got[1].ListenPorts, []uint16{20000, 20001, 20002}) {
		t.Errorf("a tags-only filter must not touch ports, got %v", got[1].ListenPorts)
	}

	// Ports: keep the intersection, drop an inbound with none of its ports.
	got = nodeProfile{ListenPorts: []int32{8443, 20000, 20002, 9}}.filterInbounds(fleet)
	if !reflect.DeepEqual(tags(got), []string{"alpha", "multi"}) {
		t.Fatalf("ports filter = %v, want [alpha multi] (beta's 2087 is not served)", tags(got))
	}
	if got[0].ListenPort != 8443 || got[0].ListenPorts != nil {
		t.Errorf("single-port inbound changed: %+v", got[0])
	}
	if got[1].ListenPort != 20002 || !reflect.DeepEqual(got[1].ListenPorts, []uint16{20000, 20002}) {
		t.Errorf("multi = %+v, want listen_port 20002 (primary kept) and listen_ports [20000 20002]", got[1])
	}

	// One port left: listen_ports disappears and listen_port is one that is served.
	got = nodeProfile{ListenPorts: []int32{20001}}.filterInbounds(fleet)
	if len(got) != 1 || got[0].ListenPort != 20001 || got[0].ListenPorts != nil {
		t.Errorf("single remaining port = %+v, want listen_port 20001 and no listen_ports", got)
	}

	// The fleet inbound must be unchanged by all of the above.
	if !reflect.DeepEqual(fleet[2].ListenPorts, []uint16{20000, 20001, 20002}) || fleet[2].ListenPort != 20002 {
		t.Errorf("filterInbounds modified the fleet inbound: %+v", fleet[2])
	}

	// Nothing served at all is a valid, empty (non-nil) list - it marshals as [].
	got = nodeProfile{ListenPorts: []int32{1}}.filterInbounds(fleet)
	if got == nil || len(got) != 0 {
		t.Errorf("no matching inbound = %#v, want an empty non-nil slice", got)
	}
	if raw, _ := json.Marshal(got); string(raw) != "[]" {
		t.Errorf("empty inbounds marshal as %s, want []", raw)
	}
}

func TestNodeProfileKeyIgnoresOrderAndSeparatesProfiles(t *testing.T) {
	a := nodeProfile{InboundTags: []string{"x", "y"}, ListenPorts: []int32{1, 2}}
	b := nodeProfile{InboundTags: []string{"y", "x"}, ListenPorts: []int32{2, 1}}
	if a.key() != b.key() {
		t.Error("the same tags and ports in another order must share a key")
	}
	if a.key() == (nodeProfile{InboundTags: []string{"x"}, ListenPorts: []int32{1, 2}}).key() {
		t.Error("different tags must not share a key")
	}
	if a.key() == (nodeProfile{InboundTags: []string{"x", "y"}}).key() {
		t.Error("different ports must not share a key")
	}
	ovA := nodeProfile{CoreOverrides: coreOverridesDTO{LogLevel: stringPtr("debug")}}
	ovB := nodeProfile{CoreOverrides: coreOverridesDTO{LogLevel: stringPtr("info")}}
	if ovA.key() == ovB.key() || ovA.key() == (nodeProfile{}).key() {
		t.Error("different overrides must not share a key")
	}
	if !(nodeProfile{}).isDefault() || (nodeProfile{}).key() != (nodeProfile{InboundTags: []string{}, ListenPorts: []int32{}}).key() {
		t.Error("nil and empty lists are both the default profile")
	}
}

func TestEtagMatches(t *testing.T) {
	const tag = `"abc"`
	for header, want := range map[string]bool{
		`"abc"`:            true,
		`W/"abc"`:          true,
		`"x", "abc"`:       true,
		`"x" ,W/"abc" , y`: true,
		`*`:                true,
		`"abcd"`:           false,
		`abc`:              false,
		``:                 false,
		`"x", "y"`:         false,
	} {
		if got := etagMatches(header, tag); got != want {
			t.Errorf("etagMatches(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestNodeConfigCacheServesOnlyTheCurrentVersionAndForgetsWhatCannotBeServed(t *testing.T) {
	var c nodeConfigCache
	fresh := func(version int64) *nodeConfigEntry {
		return &nodeConfigEntry{version: version, builtAt: time.Now(), etag: `"e"`, body: []byte("{}")}
	}

	c.store("a", fresh(7))
	if c.lookup("a", 7) == nil {
		t.Fatal("an entry stored at version 7 must be served for version 7")
	}
	if c.lookup("a", 8) != nil || c.lookup("a", 6) != nil {
		t.Error("an entry must never be served for any other data version, higher or lower")
	}
	if c.lookup("other", 7) != nil {
		t.Error("another profile's key must not hit")
	}

	// Storing at a newer version drops the older entries of every profile.
	c.store("b", fresh(7))
	c.store("a", fresh(9))
	if c.lookup("b", 7) != nil || len(c.entries) != 1 {
		t.Errorf("stale entries linger after a newer version was stored: %d entries", len(c.entries))
	}

	// The safety expiry: an entry older than the TTL is not served even at
	// the right version, and is dropped by the next store.
	old := fresh(9)
	old.builtAt = time.Now().Add(-nodeConfigSafetyTTL - time.Second)
	c.store("old", old)
	if c.lookup("old", 9) != nil {
		t.Error("an entry past the safety expiry was served")
	}
	c.store("new", fresh(9))
	if _, still := c.entries["old"]; still {
		t.Error("an expired entry survived the next store")
	}

	snap := &nodeConfigSnapshot{version: 3, builtAt: time.Now()}
	c.storeSnapshot(snap)
	if c.lookupSnapshot(3) != snap || c.lookupSnapshot(4) != nil {
		t.Error("the snapshot must be served for its own version only")
	}
	snap.builtAt = time.Now().Add(-nodeConfigSafetyTTL - time.Second)
	if c.lookupSnapshot(3) != nil {
		t.Error("an expired snapshot was served")
	}
}
