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
