package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

func TestVLESSLinkTLS(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", HeaderType: "", Port: 443, Security: "tls", SNI: "example.com", Fingerprint: "chrome", ALPN: "h2"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "uuid-1", Flow: proxysettings.FlowVision}}

	link, err := BuildLink("My Server", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("BuildLink: %v", err)
	}
	if !strings.HasPrefix(link, "vless://uuid-1@1.2.3.4:443?") {
		t.Fatalf("unexpected link prefix: %s", link)
	}
	if !strings.HasSuffix(link, "#My%20Server") {
		t.Errorf("remark not percent-encoded as expected (want %%20 for space): %s", link)
	}

	q := parseLinkQuery(t, link)
	want := map[string]string{
		"security": "tls", "type": "tcp", "headerType": "", "flow": "xtls-rprx-vision",
		"sni": "example.com", "fp": "chrome", "alpn": "h2", "path": "", "host": "",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("query param %q = %q, want %q", k, got, v)
		}
	}
}

func TestVLESSLinkFlowOmittedOnNonTCP(t *testing.T) {
	// Matches vless()'s guard: flow only appears for tls/reality + tcp/raw/kcp
	// + non-http headerType - a ws transport must never carry flow.
	in := EffectiveInbound{Network: "ws", Port: 443, Security: "tls", SNI: "example.com", Path: "/ws", HostHeader: "example.com"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "uuid-1", Flow: proxysettings.FlowVision}}

	link, _ := BuildLink("r", "1.2.3.4", in, settings)
	q := parseLinkQuery(t, link)
	if q.Has("flow") {
		t.Errorf("flow present on a ws transport, want omitted: %s", link)
	}
	if q.Get("path") != "/ws" || q.Get("host") != "example.com" {
		t.Errorf("ws path/host not set correctly: %s", link)
	}
}

func TestVLESSLinkReality(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "reality", SNI: "www.microsoft.com", Fingerprint: "chrome", RealityPublicKey: "pubkey123", RealityShortID: "ab12"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "uuid-1"}}

	link, _ := BuildLink("r", "1.2.3.4", in, settings)
	q := parseLinkQuery(t, link)
	if q.Get("pbk") != "pubkey123" || q.Get("sid") != "ab12" || q.Get("sni") != "www.microsoft.com" {
		t.Errorf("reality params missing/wrong: %s", link)
	}
	if q.Has("alpn") {
		t.Errorf("alpn should not appear for reality security: %s", link)
	}
}

func TestTrojanLinkPasswordEscaped(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls", SNI: "example.com"}
	settings := proxysettings.Settings{Type: proxysettings.Trojan, Trojan: &proxysettings.TrojanSettings{Password: "p@ss word"}}

	link, _ := BuildLink("r", "1.2.3.4", in, settings)
	if !strings.HasPrefix(link, "trojan://p%40ss%20word@1.2.3.4:443?") {
		t.Errorf("password not percent-encoded correctly: %s", link)
	}
}

func TestVMessLinkJSON(t *testing.T) {
	in := EffectiveInbound{Network: "ws", HeaderType: "none", Port: 8080, Security: "none", Path: "/vm", HostHeader: "cdn.example.com"}
	settings := proxysettings.Settings{Type: proxysettings.VMess, VMess: &proxysettings.VMessSettings{ID: "vmess-uuid"}}

	link, err := BuildLink("VMess Server", "5.6.7.8", in, settings)
	if err != nil {
		t.Fatalf("BuildLink: %v", err)
	}
	if !strings.HasPrefix(link, "vmess://") {
		t.Fatalf("missing vmess:// prefix: %s", link)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		t.Fatalf("vmess payload is not valid base64: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("vmess payload is not valid JSON: %v", err)
	}
	wantFields := map[string]interface{}{
		"add": "5.6.7.8", "port": "8080", "id": "vmess-uuid", "net": "ws",
		"path": "/vm", "host": "cdn.example.com", "ps": "VMess Server", "v": "2", "tls": "none",
	}
	for k, v := range wantFields {
		if payload[k] != v {
			t.Errorf("vmess JSON field %q = %v, want %v", k, payload[k], v)
		}
	}
}

func TestShadowsocksLink(t *testing.T) {
	in := EffectiveInbound{Port: 8388}
	settings := proxysettings.Settings{Type: proxysettings.Shadowsocks, Shadowsocks: &proxysettings.ShadowsocksSettings{Password: "secret", Method: proxysettings.Chacha20Poly1305}}

	link, err := BuildLink("SS Node", "9.9.9.9", in, settings)
	if err != nil {
		t.Fatalf("BuildLink: %v", err)
	}
	if !strings.HasPrefix(link, "ss://") || !strings.HasSuffix(link, "#SS%20Node") {
		t.Fatalf("unexpected ss link shape: %s", link)
	}
	userinfo := strings.TrimSuffix(strings.TrimPrefix(link, "ss://"), "@9.9.9.9:8388#SS%20Node")
	decoded, err := base64.StdEncoding.DecodeString(userinfo)
	if err != nil {
		t.Fatalf("ss userinfo is not valid base64: %v", err)
	}
	if string(decoded) != "chacha20-ietf-poly1305:secret" {
		t.Errorf("ss userinfo = %q, want method:password", decoded)
	}
}

func parseLinkQuery(t *testing.T, link string) url.Values {
	t.Helper()
	idx := strings.Index(link, "?")
	if idx < 0 {
		t.Fatalf("link has no query string: %s", link)
	}
	rest := link[idx+1:]
	if h := strings.Index(rest, "#"); h >= 0 {
		rest = rest[:h]
	}
	q, err := url.ParseQuery(rest)
	if err != nil {
		t.Fatalf("could not parse query from link %s: %v", link, err)
	}
	return q
}
