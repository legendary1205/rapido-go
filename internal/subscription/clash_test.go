package subscription

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

func TestClashProxyDropsVLESSOnPlainClash(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "u"}}

	node, err := ClashProxy("t", "1.2.3.4", in, settings, false)
	if err != nil {
		t.Fatalf("ClashProxy: %v", err)
	}
	if node != nil {
		t.Errorf("expected nil for VLESS on plain (non-meta) Clash, got %+v", node)
	}
}

func TestClashProxyKeepsVLESSOnClashMeta(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "reality", SNI: "example.com", Fingerprint: "chrome", RealityPublicKey: "pub", RealityShortID: "sid"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "u"}}

	node, err := ClashProxy("t", "1.2.3.4", in, settings, true)
	if err != nil {
		t.Fatalf("ClashProxy: %v", err)
	}
	if node == nil || node["type"] != "vless" {
		t.Fatalf("expected a vless node on clash-meta, got %+v", node)
	}
	realityOpts, ok := node["reality-opts"].(map[string]any)
	if !ok || realityOpts["public-key"] != "pub" || realityOpts["short-id"] != "sid" {
		t.Errorf("reality-opts wrong: %+v", node)
	}
}

func TestClashProxyDropsHysteria2AndTUICOnPlainClash(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls"}
	for _, s := range []proxysettings.Settings{
		{Type: proxysettings.Hysteria2, Hysteria2: &proxysettings.Hysteria2Settings{Password: "pw"}},
		{Type: proxysettings.TUIC, TUIC: &proxysettings.TUICSettings{ID: "u", Password: "pw"}},
	} {
		node, err := ClashProxy("t", "1.2.3.4", in, s, false)
		if err != nil {
			t.Fatalf("ClashProxy: %v", err)
		}
		if node != nil {
			t.Errorf("expected nil for %s on plain (non-meta) Clash, got %+v", s.Type, node)
		}
	}
}

func TestClashProxyHysteria2OnMeta(t *testing.T) {
	in := EffectiveInbound{Port: 443, SNI: "example.com", AllowInsecure: true, UpMbps: 50, DownMbps: 200, Hysteria2ObfsPassword: "obfs-pw"}
	settings := proxysettings.Settings{Type: proxysettings.Hysteria2, Hysteria2: &proxysettings.Hysteria2Settings{Password: "pw"}}

	node, err := ClashProxy("HY2", "1.2.3.4", in, settings, true)
	if err != nil {
		t.Fatalf("ClashProxy: %v", err)
	}
	if node == nil {
		t.Fatal("expected a node on Clash Meta")
	}
	if node["type"] != "hysteria2" || node["password"] != "pw" || node["sni"] != "example.com" ||
		node["skip-cert-verify"] != true || node["up"] != 50 || node["down"] != 200 ||
		node["obfs"] != "salamander" || node["obfs-password"] != "obfs-pw" {
		t.Errorf("hysteria2 node wrong: %+v", node)
	}
}

func TestClashProxyTUICOnMeta(t *testing.T) {
	in := EffectiveInbound{Port: 443, SNI: "example.com", CongestionControl: "bbr", ZeroRTTHandshake: true}
	settings := proxysettings.Settings{Type: proxysettings.TUIC, TUIC: &proxysettings.TUICSettings{ID: "uuid-1", Password: "pw"}}

	node, err := ClashProxy("TUIC", "1.2.3.4", in, settings, true)
	if err != nil {
		t.Fatalf("ClashProxy: %v", err)
	}
	if node == nil {
		t.Fatal("expected a node on Clash Meta")
	}
	if node["type"] != "tuic" || node["uuid"] != "uuid-1" || node["password"] != "pw" ||
		node["congestion-controller"] != "bbr" || node["reduce-rtt"] != true || node["udp-relay-mode"] != "native" {
		t.Errorf("tuic node wrong: %+v", node)
	}
}

func TestClashProxySkipsUnsupportedTransport(t *testing.T) {
	in := EffectiveInbound{Network: "xhttp", Port: 443}
	settings := proxysettings.Settings{Type: proxysettings.Trojan, Trojan: &proxysettings.TrojanSettings{Password: "p"}}

	node, err := ClashProxy("t", "1.2.3.4", in, settings, true)
	if err != nil {
		t.Fatalf("ClashProxy: %v", err)
	}
	if node != nil {
		t.Errorf("expected nil for xhttp (no Clash transport maps to it), got %+v", node)
	}
}

func TestClashProxyShadowsocksIsMinimal(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 8388, Security: "tls", SNI: "should-be-ignored.example"}
	settings := proxysettings.Settings{Type: proxysettings.Shadowsocks, Shadowsocks: &proxysettings.ShadowsocksSettings{Password: "pw", Method: "aes-256-gcm"}}

	node, err := ClashProxy("t", "1.2.3.4", in, settings, false)
	if err != nil {
		t.Fatalf("ClashProxy: %v", err)
	}
	if node["type"] != "ss" || node["password"] != "pw" || node["cipher"] != "aes-256-gcm" {
		t.Fatalf("shadowsocks node wrong: %+v", node)
	}
	if _, hasTLS := node["tls"]; hasTLS {
		t.Errorf("shadowsocks node must never carry a tls block: %+v", node)
	}
}

func TestClashConfigHasRequiredTopLevelKeys(t *testing.T) {
	raw, err := ClashConfig([]map[string]any{{"name": "a", "type": "ss"}}, "")
	if err != nil {
		t.Fatalf("ClashConfig: %v", err)
	}
	s := string(raw)
	for _, key := range []string{"proxies:", "proxy-groups:", "rules:"} {
		if !strings.Contains(s, key) {
			t.Errorf("output missing required top-level key %q:\n%s", key, s)
		}
	}
}

// TestClashConfigProxyGroupAndRuleAreFunctional is a regression test for a
// real production bug: proxy-groups/rules used to always render as empty
// lists (a bare skeleton copied from Python's ClashConfiguration.__init__
// without following through to what its .render() template actually adds
// - see ClashConfig's doc comment). A real Clash Meta client (FlClash)
// failed every connection against that output with a generic "network
// exception", because an empty proxy-groups/rules gives Clash proxies to
// look at but nothing selecting or routing through any of them. The keys
// merely existing (TestClashConfigHasRequiredTopLevelKeys, above) was
// never enough to catch this - only their content mattered.
func TestClashConfigProxyGroupAndRuleAreFunctional(t *testing.T) {
	raw, err := ClashConfig([]map[string]any{
		{"name": "Germany", "type": "vless"},
		{"name": "Finland", "type": "vless"},
	}, "")
	if err != nil {
		t.Fatalf("ClashConfig: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, raw)
	}

	groups, ok := doc["proxy-groups"].([]any)
	if !ok || len(groups) == 0 {
		t.Fatalf("proxy-groups is empty - a Clash client has nothing to select through: %+v", doc["proxy-groups"])
	}
	group, ok := groups[0].(map[string]any)
	if !ok || group["type"] != "select" {
		t.Fatalf("first proxy-group is not a select group: %+v", groups[0])
	}
	names, ok := group["proxies"].([]any)
	if !ok || len(names) != 2 || names[0] != "Germany" || names[1] != "Finland" {
		t.Errorf("proxy-group's proxies list = %+v, want [Germany Finland]", group["proxies"])
	}

	rules, ok := doc["rules"].([]any)
	if !ok || len(rules) == 0 {
		t.Fatalf("rules is empty - a Clash client has no route for any traffic: %+v", doc["rules"])
	}
	last := rules[len(rules)-1]
	if s, ok := last.(string); !ok || !strings.HasPrefix(s, "MATCH,") {
		t.Errorf("last rule = %+v, want a MATCH,<group> catch-all", last)
	}
}
