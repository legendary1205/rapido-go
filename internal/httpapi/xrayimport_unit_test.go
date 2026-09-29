package httpapi

import (
	"testing"

	"github.com/legendary1205/rapido-go/internal/xrayimport"
)

func TestAppendNewRoutingRulesIsIdempotent(t *testing.T) {
	file := []xrayimport.RoutingRule{
		{Inbound: []string{"de"}, OutboundTag: "de"},
		{Inbound: []string{"fi"}, OutboundTag: "fi"},
	}

	once := appendNewRoutingRules(nil, file)
	if len(once) != 2 {
		t.Fatalf("first import: %d rules, want 2", len(once))
	}
	// A retried migration or a second click sends the same file again.
	for i := 0; i < 3; i++ {
		once = appendNewRoutingRules(once, file)
	}
	if len(once) != 2 {
		t.Errorf("after re-importing the same file 3 times: %d rules, want 2", len(once))
	}
}

func TestAppendNewRoutingRulesKeepsDifferentRulesInOrder(t *testing.T) {
	existing := []routingRuleDTO{
		{Inbound: []string{"de"}, OutboundTag: "de"},
		{DomainSuffix: []string{"example.com"}, OutboundTag: "blackhole"},
	}
	imported := []xrayimport.RoutingRule{
		{Inbound: []string{"de"}, OutboundTag: "de"},                      // already there
		{Inbound: []string{"de"}, OutboundTag: "nl"},                      // same match, different exit: a different rule
		{DomainSuffix: []string{"example.com"}, OutboundTag: "blackhole"}, // already there
		{Inbound: []string{"us"}, Network: []string{}, OutboundTag: "us"},
		{Inbound: []string{"us"}, OutboundTag: "us"}, // same as the previous one once an empty list is ignored
	}

	got := appendNewRoutingRules(existing, imported)

	want := []string{"de", "blackhole", "nl", "us"}
	if len(got) != len(want) {
		t.Fatalf("got %d rules, want %d: %+v", len(got), len(want), got)
	}
	for i, r := range got {
		if r.OutboundTag != want[i] {
			t.Errorf("rule %d routes to %q, want %q", i, r.OutboundTag, want[i])
		}
	}
}
