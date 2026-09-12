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
	if !ok || len(outbounds) != 3 { // 2 proxies + 1 selector
		t.Fatalf("expected 3 outbounds (2 proxies + selector), got %+v", doc["outbounds"])
	}
	selector := outbounds[2].(map[string]any)
	if selector["type"] != "selector" || selector["default"] != "node-a" {
		t.Errorf("selector outbound wrong: %+v", selector)
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
