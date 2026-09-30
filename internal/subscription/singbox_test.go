package subscription

import (
	"encoding/json"
	"testing"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

func TestSingBoxOutboundVLESSReality(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "reality", SNI: "www.microsoft.com", Fingerprint: "chrome", RealityPublicKey: "pub", RealityShortID: "sid1"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "uuid-1", Flow: proxysettings.FlowVision}}

	out, err := SingBoxOutbound("My Node", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if out["type"] != "vless" || out["uuid"] != "uuid-1" || out["flow"] != "xtls-rprx-vision" {
		t.Errorf("core fields wrong: %+v", out)
	}
	tls, ok := out["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls block missing: %+v", out)
	}
	reality, ok := tls["reality"].(map[string]any)
	if !ok || reality["public_key"] != "pub" || reality["short_id"] != "sid1" {
		t.Errorf("reality block wrong: %+v", tls)
	}
}

func TestSingBoxOutboundHysteria2(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls", SNI: "example.com", UpMbps: 50, DownMbps: 200, Hysteria2ObfsPassword: "obfs-pw"}
	settings := proxysettings.Settings{Type: proxysettings.Hysteria2, Hysteria2: &proxysettings.Hysteria2Settings{Password: "pw"}}

	out, err := SingBoxOutbound("HY2", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if out["type"] != "hysteria2" || out["password"] != "pw" || out["up_mbps"] != 50 || out["down_mbps"] != 200 {
		t.Errorf("core fields wrong: %+v", out)
	}
	obfs, ok := out["obfs"].(map[string]any)
	if !ok || obfs["type"] != "salamander" || obfs["password"] != "obfs-pw" {
		t.Errorf("obfs block wrong: %+v", out)
	}
	tls, ok := out["tls"].(map[string]any)
	if !ok || tls["enabled"] != true || tls["server_name"] != "example.com" {
		t.Errorf("tls block wrong: %+v", out)
	}
	if _, present := out["multiplex"]; present {
		t.Errorf("hysteria2 has no multiplex field in sing-box's own option struct: %+v", out)
	}
	if _, present := out["transport"]; present {
		t.Errorf("hysteria2 has no transport of its own: %+v", out)
	}
}

func TestSingBoxOutboundTUIC(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls", SNI: "example.com", CongestionControl: "bbr", ZeroRTTHandshake: true}
	settings := proxysettings.Settings{Type: proxysettings.TUIC, TUIC: &proxysettings.TUICSettings{ID: "uuid-1", Password: "pw"}}

	out, err := SingBoxOutbound("TUIC", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if out["type"] != "tuic" || out["uuid"] != "uuid-1" || out["password"] != "pw" ||
		out["congestion_control"] != "bbr" || out["zero_rtt_handshake"] != true {
		t.Errorf("core fields wrong: %+v", out)
	}
	if _, present := out["multiplex"]; present {
		t.Errorf("tuic has no multiplex field in sing-box's own option struct: %+v", out)
	}
}

func TestSingBoxOutboundSnell(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 2000, SnellPSK: "correct-horse-battery-staple", SnellV6Mode: "unshaped"}
	settings := proxysettings.Settings{Type: proxysettings.Snell, Snell: &proxysettings.SnellSettings{UserKey: "key-1"}}

	out, err := SingBoxOutbound("Snell", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if out["type"] != "snell" || out["psk"] != "correct-horse-battery-staple" || out["version"] != 6 ||
		out["userkey"] != "key-1" || out["mode"] != "unshaped" {
		t.Errorf("core fields wrong: %+v", out)
	}
	if _, present := out["multiplex"]; present {
		t.Errorf("snell has no multiplex field in sing-box's own option struct: %+v", out)
	}
	if _, present := out["tls"]; present {
		t.Errorf("snell has no TLS of its own: %+v", out)
	}
}

func TestSingBoxOutboundSnellOmitsModeWhenDefault(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 2000, SnellPSK: "correct-horse-battery-staple"}
	settings := proxysettings.Settings{Type: proxysettings.Snell, Snell: &proxysettings.SnellSettings{UserKey: "key-1"}}

	out, err := SingBoxOutbound("Snell", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if _, present := out["mode"]; present {
		t.Errorf("mode present although snell_v6_mode was unset (default): %+v", out)
	}
}

func TestSingBoxOutboundTUICDefaultsCongestionControlToCubic(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls", SNI: "example.com"}
	settings := proxysettings.Settings{Type: proxysettings.TUIC, TUIC: &proxysettings.TUICSettings{ID: "uuid-1", Password: "pw"}}

	out, err := SingBoxOutbound("TUIC", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if out["congestion_control"] != "cubic" {
		t.Errorf("congestion_control = %v, want the node's own default cubic", out["congestion_control"])
	}
	if _, present := out["zero_rtt_handshake"]; present {
		t.Errorf("zero_rtt_handshake present although ZeroRTTHandshake was false: %+v", out)
	}
}

func TestSingBoxOutboundSkipsUnsupportedTransport(t *testing.T) {
	in := EffectiveInbound{Network: "xhttp", Port: 443}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "u"}}
	out, err := SingBoxOutbound("t", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil for unsupported xhttp transport, got %+v", out)
	}
}

func TestSingBoxConfigIsValidJSONWithSelector(t *testing.T) {
	raw, err := SingBoxConfig([]map[string]any{
		{"type": "vless", "tag": "node-a"},
		{"type": "trojan", "tag": "node-b"},
	})
	if err != nil {
		t.Fatalf("SingBoxConfig: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	outbounds, ok := doc["outbounds"].([]any)
	if !ok || len(outbounds) != 4 { // 2 proxies + selector + direct
		t.Fatalf("expected 4 outbounds (2 proxies + selector + direct), got %+v", doc["outbounds"])
	}
	selector := outbounds[2].(map[string]any)
	if selector["type"] != "selector" || selector["default"] != "node-a" {
		t.Errorf("selector outbound wrong: %+v", selector)
	}
	if direct := outbounds[3].(map[string]any); direct["type"] != "direct" {
		t.Errorf("expected a direct outbound at index 3, got %+v", direct)
	}
}

// TestSingBoxConfigHasAWorkingStandaloneRoute is a regression test for a
// real production bug: without an inbound and a default route, the document
// this function renders dialed fine for a client's own per-outbound latency
// probe (which needs neither) but never actually captured or routed any of
// the device's real traffic - see this function's own doc comment.
func TestSingBoxConfigHasAWorkingStandaloneRoute(t *testing.T) {
	raw, err := SingBoxConfig([]map[string]any{{"type": "vless", "tag": "node-a"}})
	if err != nil {
		t.Fatalf("SingBoxConfig: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	inbounds, ok := doc["inbounds"].([]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("expected exactly one inbound (the tun), got %+v", doc["inbounds"])
	}
	tun := inbounds[0].(map[string]any)
	if tun["type"] != "tun" || tun["auto_route"] != true {
		t.Errorf("tun inbound wrong: %+v", tun)
	}
	// A regression test of its own: InboundOptions.SniffEnabled is a legacy
	// per-inbound bool sing-box 1.13 removed outright - setting it here made
	// a current client reject the whole document at load time instead of
	// merely not sniffing. Sniffing is expressed as a route rule/action
	// instead (checked below).
	if _, present := tun["sniff"]; present {
		t.Errorf("tun inbound still sets the legacy \"sniff\" field: %+v", tun)
	}
	route, ok := doc["route"].(map[string]any)
	if !ok || route["final"] != "proxy" {
		t.Fatalf("route.final = %v, want \"proxy\" so real traffic actually goes through the selector", route["final"])
	}
	// Every outbound's address is a domain, not an IP - without an explicit
	// resolver, sing-box 1.12+ warns ("missing route.default_domain_resolver
	// or domain_resolver in dial fields") on every one of them.
	if route["default_domain_resolver"] != "dns-direct" {
		t.Errorf("route.default_domain_resolver = %v, want \"dns-direct\"", route["default_domain_resolver"])
	}
	rules, _ := route["rules"].([]any)
	sniffRuleFound := false
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		if rule["action"] == "sniff" && rule["inbound"] == "tun-in" {
			sniffRuleFound = true
		}
	}
	if !sniffRuleFound {
		t.Error("no sniff route rule for tun-in - sniffing needs this now that the legacy inbound field is gone")
	}
	dns, ok := doc["dns"].(map[string]any)
	if !ok {
		t.Fatal("dns section missing - without it, domain resolution over the tun has nothing to use")
	}
	// Another regression test in the same vein as the inbound "sniff" one
	// above: an "ip_is_private"-style filter directly on a DNS rule is a
	// legacy address-filter field sing-box 1.14 deprecated (it now needs the
	// newer evaluate/match_response shape instead) - this profile has no
	// need for that complexity, so it must carry no DNS rules at all rather
	// than a legacy one.
	if _, present := dns["rules"]; present {
		t.Errorf("dns.rules should be absent (no legacy address-filter rules), got %+v", dns["rules"])
	}
	if tun["stack"] != nil {
		t.Errorf("tun inbound still sets \"stack\" = %v - sing-box 1.15 deprecated picking one explicitly", tun["stack"])
	}
}

// TestSingBoxOutboundSplitsMultiValueALPN is a regression test for a real
// production bug: a multi-value ALPN like "h2,http/1.1" (validAlpn in
// internal/httpapi/hosts.go allows exactly this comma-joined shape as one
// stored value) was wrapped as a single one-element array
// (`["h2,http/1.1"]`) instead of split into
// separate protocol names (`["h2","http/1.1"]`). sing-box's TLS layer
// treats each array element as one protocol identifier - a client
// offering the single malformed string as its ALPN doesn't match "h2" or
// "http/1.1" server-side, and strict ALPN negotiation (RFC 7301) aborts
// the handshake entirely. This was found live: every client on a format
// that hit this path failed every connection after a real migration.
func TestSingBoxOutboundSplitsMultiValueALPN(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls", SNI: "example.com", ALPN: "h2,http/1.1"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "uuid-1"}}

	out, err := SingBoxOutbound("My Node", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	tls, ok := out["tls"].(map[string]any)
	if !ok {
		t.Fatalf("no tls block: %+v", out)
	}
	alpn, ok := tls["alpn"].([]string)
	if !ok || len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Errorf("alpn = %+v, want [h2 http/1.1] as separate entries", tls["alpn"])
	}
}
