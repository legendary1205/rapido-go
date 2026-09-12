package subscription

import (
	"encoding/json"
	"testing"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

func TestV2rayJSONConfigVLESSReality(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "reality", SNI: "example.com", Fingerprint: "chrome", RealityPublicKey: "pub", RealityShortID: "sid"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "uuid-1", Flow: proxysettings.FlowVision}}

	cfg, err := V2rayJSONConfig("My Node", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("V2rayJSONConfig: %v", err)
	}
	if cfg["remarks"] != "My Node" {
		t.Errorf("remarks = %v, want My Node", cfg["remarks"])
	}
	outbounds := cfg["outbounds"].([]map[string]any)
	if len(outbounds) != 3 {
		t.Fatalf("expected 3 outbounds (proxy + direct + block), got %d", len(outbounds))
	}
	proxy := outbounds[0]
	if proxy["protocol"] != "vless" {
		t.Errorf("proxy protocol = %v, want vless", proxy["protocol"])
	}
	settingsMap := proxy["settings"].(map[string]any)
	vnext := settingsMap["vnext"].([]map[string]any)
	user := vnext[0]["users"].([]map[string]any)[0]
	if user["id"] != "uuid-1" || user["flow"] != "xtls-rprx-vision" {
		t.Errorf("vless user wrong: %+v", user)
	}
	stream := proxy["streamSettings"].(map[string]any)
	if stream["security"] != "reality" {
		t.Errorf("stream security = %v, want reality", stream["security"])
	}
	realitySettings := stream["realitySettings"].(map[string]any)
	if realitySettings["publicKey"] != "pub" || realitySettings["shortId"] != "sid" {
		t.Errorf("realitySettings wrong: %+v", realitySettings)
	}

	// Round trip through real JSON to catch any non-marshalable value.
	if _, err := json.Marshal(cfg); err != nil {
		t.Fatalf("config does not marshal to JSON: %v", err)
	}
}

// TestV2rayJSONConfigSplitsMultiValueALPN is a regression test for a real
// production bug affecting every v2rayNG/v2rayN/Streisand/Happ user: a
// multi-value ALPN like "h2,http/1.1" was wrapped as a single one-element
// array (`["h2,http/1.1"]`) instead of split into separate protocol names
// (`["h2","http/1.1"]`). xray-core's TLS layer treats each array element
// as one protocol identifier - a client offering the single malformed
// string doesn't match "h2" or "http/1.1" server-side, and strict ALPN
// negotiation aborts the handshake entirely. Found live: every client on
// this format failed every connection after a real migration.
func TestV2rayJSONConfigSplitsMultiValueALPN(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls", SNI: "example.com", ALPN: "h2,http/1.1"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "uuid-1"}}

	cfg, err := V2rayJSONConfig("My Node", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("V2rayJSONConfig: %v", err)
	}
	outbounds := cfg["outbounds"].([]map[string]any)
	stream := outbounds[0]["streamSettings"].(map[string]any)
	tlsSettings := stream["tlsSettings"].(map[string]any)
	alpn, ok := tlsSettings["alpn"].([]string)
	if !ok || len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Errorf("alpn = %+v, want [h2 http/1.1] as separate entries", tlsSettings["alpn"])
	}
}

func TestV2rayJSONConfigSkipsUnsupportedTransport(t *testing.T) {
	in := EffectiveInbound{Network: "kcp", Port: 443}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "u"}}

	cfg, err := V2rayJSONConfig("t", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("V2rayJSONConfig: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil for kcp (EffectiveInbound has no fields to describe it), got %+v", cfg)
	}
}

func TestV2rayJSONConfigMuxSkippedWithVisionFlow(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls", SNI: "example.com", MuxEnable: true}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "u", Flow: proxysettings.FlowVision}}

	cfg, err := V2rayJSONConfig("t", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("V2rayJSONConfig: %v", err)
	}
	outbounds := cfg["outbounds"].([]map[string]any)
	if _, hasMux := outbounds[0]["mux"]; hasMux {
		t.Error("a VLESS outbound with Vision flow must never carry a mux block - the server rejects a muxed Vision request")
	}
}

func TestV2rayJSONArrayIsRealJSONArray(t *testing.T) {
	raw, err := V2rayJSONArray([]map[string]any{{"remarks": "a"}, {"remarks": "b"}})
	if err != nil {
		t.Fatalf("V2rayJSONArray: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, raw)
	}
	if len(arr) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(arr))
	}
}
