package xrayimport

import (
	"strings"
	"testing"
)

// fakeCertJSON/fakeKeyJSON are a synthetic, self-signed test cert/key -
// never a real one. Matches this project's own established rule (see
// internal/legacyimport's real-sample tests) of never committing real
// certificate/customer material to the repo.
const fakeCertLine1 = "-----BEGIN CERTIFICATE-----"
const fakeCertLine2 = "MIIBXTCCAQOgAwIBAgIBATAKBggqhkjOPQQDAjAXMRUwEwYDVQQDEwxleGFtcGxl"
const fakeCertLine3 = "-----END CERTIFICATE-----"
const fakeKeyLine1 = "-----BEGIN EC PRIVATE KEY-----"
const fakeKeyLine2 = "MHcCAQEEICiESJZ2LnzP+hpr9U26Puzb5DoiaYNIH/vBsj4wAoDvoAoGCCqGSM49"
const fakeKeyLine3 = "-----END EC PRIVATE KEY-----"

// sampleConfig mirrors the shape (not the real content) of the real
// production config this importer was built against: a multi-port
// "node1" inbound (2 ports instead of 4, enough to prove splitting), a
// single-port "node2" inbound, real TLS on node1, one dropped fallback,
// two freedom outbounds bound to different interfaces, a blackhole
// outbound, routing rules with and without localPort, and a plain-string
// DNS server list.
const sampleConfig = `{
  "log": {"loglevel": "warning"},
  "dns": {"servers": ["9.9.9.10", "149.112.112.10"]},
  "inbounds": [
    {
      "tag": "node1",
      "port": "20000,20004",
      "protocol": "vless",
      "settings": {"clients": [], "decryption": "none", "fallbacks": [{"dest": 80}]},
      "streamSettings": {
        "network": "tcp",
        "security": "tls",
        "tlsSettings": {
          "serverName": "example.test",
          "fingerprint": "chrome",
          "alpn": ["h3", "h2"],
          "minVersion": "1.2",
          "certificates": [{"usage": "encipherment",
            "certificate": ["` + fakeCertLine1 + `", "` + fakeCertLine2 + `", "` + fakeCertLine3 + `"],
            "key": ["` + fakeKeyLine1 + `", "` + fakeKeyLine2 + `", "` + fakeKeyLine3 + `"]}]
        },
        "tcpSettings": {"header": {"type": "none"}}
      },
      "sniffing": {"enabled": true, "destOverride": ["http", "tls"]}
    },
    {
      "tag": "node2",
      "port": 20001,
      "protocol": "vless",
      "settings": {"clients": [], "decryption": "none"},
      "streamSettings": {"network": "tcp", "security": "none"}
    }
  ],
  "outbounds": [
    {"protocol": "freedom", "tag": "DIRECT", "settings": {"domainStrategy": "ForceIPv4v6"}},
    {"protocol": "blackhole", "tag": "blackhole"},
    {"tag": "germany", "protocol": "freedom", "streamSettings": {"sockopt": {"tcpFastOpen": true, "interface": "germany"}}}
  ],
  "routing": {
    "domainStrategy": "IPIfNonMatch",
    "rules": [
      {"type": "field", "inboundTag": ["node1"], "localPort": "20000", "outboundTag": "germany"},
      {"type": "field", "inboundTag": ["node1"], "outboundTag": "blackhole"}
    ]
  }
}`

func TestParseXrayConfigSplitsAMultiPortInboundIntoOneTagPerPort(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	var node1Tags []string
	for _, in := range result.Inbounds {
		if strings.HasPrefix(in.Tag, "node1") {
			node1Tags = append(node1Tags, in.Tag)
		}
	}
	if len(node1Tags) != 2 {
		t.Fatalf("node1 split tags = %v, want 2 (one per port)", node1Tags)
	}
	want := map[string]bool{"node1-20000": true, "node1-20004": true}
	for _, tag := range node1Tags {
		if !want[tag] {
			t.Errorf("unexpected split tag %q", tag)
		}
	}
}

func TestParseXrayConfigDoesNotSplitASinglePortInbound(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	var found bool
	for _, in := range result.Inbounds {
		if in.Tag == "node2" {
			found = true
			if in.Port != 20001 {
				t.Errorf("node2 port = %d, want 20001", in.Port)
			}
		}
	}
	if !found {
		t.Fatal("expected a single 'node2' tag (no port suffix) for a single-port inbound")
	}
}

func TestParseXrayConfigCarriesRealCertificateAndServerName(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	for _, in := range result.Inbounds {
		if !strings.HasPrefix(in.Tag, "node1") {
			continue
		}
		wantCert := fakeCertLine1 + "\n" + fakeCertLine2 + "\n" + fakeCertLine3
		if in.TLSCertificate != wantCert {
			t.Errorf("%s: tls certificate = %q, want the joined PEM lines", in.Tag, in.TLSCertificate)
		}
		if in.TLSServerName != "example.test" {
			t.Errorf("%s: tls_server_name = %q, want example.test", in.Tag, in.TLSServerName)
		}
		if in.HostALPN != "h3,h2" {
			t.Errorf("%s: host alpn = %q, want h3,h2 (a valid combination)", in.Tag, in.HostALPN)
		}
		if in.HostFingerprint != "chrome" {
			t.Errorf("%s: host fingerprint = %q, want chrome", in.Tag, in.HostFingerprint)
		}
	}
}

func TestParseXrayConfigWarnsAboutDroppedFallbacksAndMinVersion(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	joined := strings.Join(result.Warnings, " | ")
	if !strings.Contains(joined, "fallback") {
		t.Errorf("expected a warning about dropped fallbacks, got: %v", result.Warnings)
	}
	if !strings.Contains(joined, "minVersion") {
		t.Errorf("expected a warning about dropped minVersion, got: %v", result.Warnings)
	}
}

func TestParseXrayConfigMapsSniffingToGlobalToggle(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	if !result.SniffEnabled {
		t.Error("expected the global sniff_enabled to be true, since node1 had sniffing.enabled=true")
	}
}

func TestParseXrayConfigMapsOutboundsAndBindInterface(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	// DIRECT (freedom) and blackhole are dropped: their tags collide with
	// this codebase's own reserved "direct"/"block" only after type
	// mapping - "DIRECT" (different case) and "blackhole" (different
	// spelling) don't actually collide, so both should come through as
	// real custom outbounds.
	byTag := map[string]Outbound{}
	for _, ob := range result.Outbounds {
		byTag[ob.Tag] = ob
	}
	if byTag["DIRECT"].Type != "direct" {
		t.Errorf("DIRECT outbound type = %q, want direct", byTag["DIRECT"].Type)
	}
	if byTag["blackhole"].Type != "block" {
		t.Errorf("blackhole outbound type = %q, want block", byTag["blackhole"].Type)
	}
	if byTag["germany"].BindInterface != "germany" {
		t.Errorf("germany outbound bind_interface = %q, want germany", byTag["germany"].BindInterface)
	}
}

func TestParseXrayConfigResolvesRoutingRulesByPortAndByWholeTag(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	if len(result.RoutingRules) != 2 {
		t.Fatalf("routing rules = %v, want 2", result.RoutingRules)
	}
	// Rule 1: node1 + localPort 20000 -> exactly the "node1-20000" split tag.
	r1 := result.RoutingRules[0]
	if r1.OutboundTag != "germany" || len(r1.Inbound) != 1 || r1.Inbound[0] != "node1-20000" {
		t.Errorf("rule 1 = %+v, want inbound=[node1-20000] outbound_tag=germany", r1)
	}
	// Rule 2: node1, no localPort -> every split tag node1 produced.
	r2 := result.RoutingRules[1]
	if r2.OutboundTag != "blackhole" || len(r2.Inbound) != 2 {
		t.Errorf("rule 2 = %+v, want both node1 split tags, outbound_tag=blackhole", r2)
	}
}

func TestParseXrayConfigParsesPlainStringDNSServers(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	if len(result.DNSServers) != 2 {
		t.Fatalf("dns servers = %v, want 2", result.DNSServers)
	}
	if result.DNSServers[0].Address != "9.9.9.10" || result.DNSServers[0].Type != "udp" {
		t.Errorf("dns server 0 = %+v, want address=9.9.9.10 type=udp", result.DNSServers[0])
	}
}

func TestParseXrayConfigMapsLogLevel(t *testing.T) {
	result, err := ParseXrayConfig([]byte(sampleConfig))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	if result.LogLevel != "warn" {
		t.Errorf("log_level = %q, want warn (mapped from Xray's \"warning\")", result.LogLevel)
	}
}

func TestParseXrayConfigSkipsUnsupportedInboundProtocolWithWarning(t *testing.T) {
	cfg := `{"inbounds":[{"tag":"dokodemo","port":1080,"protocol":"dokodemo-door","settings":{}}]}`
	result, err := ParseXrayConfig([]byte(cfg))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	if len(result.Inbounds) != 0 {
		t.Errorf("expected the unsupported-protocol inbound to be skipped entirely, got %v", result.Inbounds)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "dokodemo-door") {
		t.Errorf("expected exactly one warning naming the unsupported protocol, got %v", result.Warnings)
	}
}

func TestParseXrayConfigSkipsPortRangeWithWarning(t *testing.T) {
	cfg := `{"inbounds":[{"tag":"ranged","port":"1000-2000","protocol":"vless","settings":{}}]}`
	result, err := ParseXrayConfig([]byte(cfg))
	if err != nil {
		t.Fatalf("ParseXrayConfig: %v", err)
	}
	if len(result.Inbounds) != 0 {
		t.Errorf("a pure port-range inbound has no usable single ports, expected it skipped entirely: %v", result.Inbounds)
	}
	joined := strings.Join(result.Warnings, " | ")
	if !strings.Contains(joined, "range") {
		t.Errorf("expected a warning mentioning the unsupported port range, got %v", result.Warnings)
	}
}

func TestParseXrayConfigRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseXrayConfig([]byte("not json")); err == nil {
		t.Error("expected an error for invalid JSON input")
	}
}

func TestParseXrayConfigRejectsEmptyConfig(t *testing.T) {
	if _, err := ParseXrayConfig([]byte(`{}`)); err == nil {
		t.Error("expected an error for a config with no inbounds or outbounds at all")
	}
}
