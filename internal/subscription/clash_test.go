package subscription

import (
	"strings"
	"testing"

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
	raw, err := ClashConfig([]map[string]any{{"name": "a", "type": "ss"}})
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
