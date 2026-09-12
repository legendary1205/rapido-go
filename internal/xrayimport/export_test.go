package xrayimport

import (
	"encoding/json"
	"testing"
)

func TestBuildXrayJSONRendersTLSAndRealityInbounds(t *testing.T) {
	inbounds := []ExportInbound{
		{
			Tag: "tls-in", Protocol: "vless", Network: "tcp", Security: "tls",
			TLSCertificate: fakeCertLine1 + "\n" + fakeCertLine2 + "\n" + fakeCertLine3,
			TLSKey:         fakeKeyLine1 + "\n" + fakeKeyLine2 + "\n" + fakeKeyLine3,
			TLSServerName:  "example.test",
			Port:           20000,
			Clients: []ExportClient{
				{Email: "alice", UUID: "11111111-1111-1111-1111-111111111111", Flow: "xtls-rprx-vision"},
			},
		},
		{
			Tag: "reality-in", Protocol: "vless", Network: "tcp", Security: "reality",
			RealityPrivateKey: "priv-key", RealityShortIDs: []string{"ab12"},
			RealityServerName: "www.example.com", RealityServerPort: 443,
			Port: 20001,
			Clients: []ExportClient{
				{Email: "bob", UUID: "22222222-2222-2222-2222-222222222222"},
			},
		},
		{
			Tag: "trojan-in", Protocol: "trojan", Network: "tcp", Security: "none",
			Port: 20002,
			Clients: []ExportClient{
				{Email: "carol", Password: "s3cr3t"},
			},
		},
	}

	raw, err := BuildXrayJSON(inbounds, nil, nil, []DNSServer{{Address: "1.1.1.1"}}, "info", true)
	if err != nil {
		t.Fatalf("BuildXrayJSON: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	xInbounds, ok := doc["inbounds"].([]any)
	if !ok || len(xInbounds) != 3 {
		t.Fatalf("expected 3 inbounds, got %v", doc["inbounds"])
	}

	tlsIn := xInbounds[0].(map[string]any)
	if tlsIn["port"].(float64) != 20000 {
		t.Errorf("tls inbound port = %v, want 20000", tlsIn["port"])
	}
	stream := tlsIn["streamSettings"].(map[string]any)
	if stream["security"] != "tls" {
		t.Errorf("tls inbound security = %v, want tls", stream["security"])
	}
	tlsSettings := stream["tlsSettings"].(map[string]any)
	if tlsSettings["serverName"] != "example.test" {
		t.Errorf("tls serverName = %v, want example.test", tlsSettings["serverName"])
	}
	clients := tlsIn["settings"].(map[string]any)["clients"].([]any)
	if len(clients) != 1 || clients[0].(map[string]any)["id"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("tls inbound clients = %v", clients)
	}
	if clients[0].(map[string]any)["flow"] != "xtls-rprx-vision" {
		t.Errorf("tls inbound client flow = %v, want xtls-rprx-vision", clients[0].(map[string]any)["flow"])
	}

	realityIn := xInbounds[1].(map[string]any)
	realityStream := realityIn["streamSettings"].(map[string]any)
	if realityStream["security"] != "reality" {
		t.Errorf("reality inbound security = %v, want reality", realityStream["security"])
	}
	reality := realityStream["realitySettings"].(map[string]any)
	if reality["privateKey"] != "priv-key" {
		t.Errorf("reality privateKey = %v, want priv-key", reality["privateKey"])
	}
	if reality["dest"] != "www.example.com:443" {
		t.Errorf("reality dest = %v, want www.example.com:443", reality["dest"])
	}

	trojanIn := xInbounds[2].(map[string]any)
	trojanClients := trojanIn["settings"].(map[string]any)["clients"].([]any)
	if trojanClients[0].(map[string]any)["password"] != "s3cr3t" {
		t.Errorf("trojan client password = %v, want s3cr3t", trojanClients[0].(map[string]any)["password"])
	}

	dns := doc["dns"].(map[string]any)
	servers := dns["servers"].([]any)
	if len(servers) != 1 || servers[0] != "1.1.1.1" {
		t.Errorf("dns servers = %v, want [1.1.1.1]", servers)
	}

	if doc["log"].(map[string]any)["loglevel"] != "info" {
		t.Errorf("loglevel = %v, want info", doc["log"].(map[string]any)["loglevel"])
	}
}

func TestBuildXrayJSONOmitsUnknownOutboundTypes(t *testing.T) {
	outbounds := []Outbound{
		{Tag: "direct-out", Type: "direct"},
		{Tag: "block-out", Type: "block"},
		{Tag: "urltest-out", Type: "urltest", Outbounds: []string{"direct-out"}},
		{Tag: "vless-out", Type: "vless", Server: "1.2.3.4", ServerPort: 443, UUID: "uuid-1"},
	}

	raw, err := BuildXrayJSON(nil, outbounds, nil, nil, "", false)
	if err != nil {
		t.Fatalf("BuildXrayJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	xOutbounds := doc["outbounds"].([]any)
	if len(xOutbounds) != 3 {
		t.Fatalf("expected 3 outbounds (urltest dropped), got %d: %v", len(xOutbounds), xOutbounds)
	}
	protocols := map[string]bool{}
	for _, ob := range xOutbounds {
		protocols[ob.(map[string]any)["protocol"].(string)] = true
	}
	if !protocols["freedom"] || !protocols["blackhole"] || !protocols["vless"] {
		t.Errorf("expected freedom/blackhole/vless protocols, got %v", protocols)
	}
}

func TestBuildXrayJSONRoutingRuleFieldMapping(t *testing.T) {
	rules := []RoutingRule{
		{
			Inbound: []string{"tls-in"}, OutboundTag: "germany",
			Domain: []string{"example.com"}, DomainSuffix: []string{"ir"}, DomainKeyword: []string{"ads"},
			IPCIDR: []string{"10.0.0.0/8"}, IPIsPrivate: true,
			Port: []int{80}, PortRange: []string{"1000-2000"},
			Network: []string{"tcp", "udp"}, Protocol: []string{"http"},
		},
	}
	raw, err := BuildXrayJSON(nil, nil, rules, nil, "", false)
	if err != nil {
		t.Fatalf("BuildXrayJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	routing := doc["routing"].(map[string]any)
	xRules := routing["rules"].([]any)
	rule := xRules[0].(map[string]any)
	if rule["outboundTag"] != "germany" {
		t.Errorf("outboundTag = %v, want germany", rule["outboundTag"])
	}
	domain := rule["domain"].([]any)
	foundFull, foundSuffix, foundKeyword := false, false, false
	for _, d := range domain {
		switch d {
		case "full:example.com":
			foundFull = true
		case "domain:ir":
			foundSuffix = true
		case "ads":
			foundKeyword = true
		}
	}
	if !foundFull || !foundSuffix || !foundKeyword {
		t.Errorf("domain = %v, missing expected entries", domain)
	}
	ip := rule["ip"].([]any)
	if len(ip) != 2 { // 10.0.0.0/8 + geoip:private
		t.Errorf("ip = %v, want 2 entries", ip)
	}
	if rule["network"] != "tcp,udp" {
		t.Errorf("network = %v, want tcp,udp", rule["network"])
	}
	if rule["port"] != "80,1000-2000" {
		t.Errorf("port = %v, want 80,1000-2000", rule["port"])
	}
}
