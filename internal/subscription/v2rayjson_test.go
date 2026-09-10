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
