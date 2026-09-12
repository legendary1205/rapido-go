package subscription

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

func TestLoadYAMLTemplateMissingPathReturnsNotOK(t *testing.T) {
	if _, ok := LoadYAMLTemplate(""); ok {
		t.Error("empty path should return ok=false")
	}
	if _, ok := LoadYAMLTemplate(filepath.Join(t.TempDir(), "does-not-exist.yml")); ok {
		t.Error("nonexistent file should return ok=false")
	}
}

func TestLoadJSONTemplateMissingPathReturnsNotOK(t *testing.T) {
	if _, ok := LoadJSONTemplate(""); ok {
		t.Error("empty path should return ok=false")
	}
	if _, ok := LoadJSONTemplate(filepath.Join(t.TempDir(), "does-not-exist.json")); ok {
		t.Error("nonexistent file should return ok=false")
	}
}

// TestClashConfigUsesRealTemplateFile is the merge-path regression test:
// with a configured template file, ClashConfig must preserve every static
// key from the file (mixed-port, dns, rule-providers, rules - whatever an
// operator's file carries) and ONLY replace "proxies" and each
// proxy-group's own "proxies" placeholder with the real, current data.
func TestClashConfigUsesRealTemplateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clash.yml")
	content := `
mixed-port: 10801
dns:
  enable: true
proxy-groups:
- name: Net Shield
  type: select
  proxies: []
rules:
- RULE-SET,ads,REJECT
- MATCH,Net Shield
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw, err := ClashConfig([]map[string]any{{"name": "Germany", "type": "vless"}}, path)
	if err != nil {
		t.Fatalf("ClashConfig: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, raw)
	}
	if doc["mixed-port"] != uint64(10801) {
		t.Errorf("mixed-port from the template was not preserved: %+v", doc["mixed-port"])
	}
	dns, ok := doc["dns"].(map[string]any)
	if !ok || dns["enable"] != true {
		t.Errorf("dns block from the template was not preserved: %+v", doc["dns"])
	}
	rules, ok := doc["rules"].([]any)
	if !ok || len(rules) != 2 || rules[0] != "RULE-SET,ads,REJECT" {
		t.Errorf("rules from the template were not preserved verbatim: %+v", doc["rules"])
	}
	groups := doc["proxy-groups"].([]any)
	group := groups[0].(map[string]any)
	if group["name"] != "Net Shield" {
		t.Errorf("proxy-group name from the template was not preserved: %+v", group["name"])
	}
	names := group["proxies"].([]any)
	if len(names) != 1 || names[0] != "Germany" {
		t.Errorf("proxy-group's proxies placeholder was not filled with real data: %+v", names)
	}
}

// TestV2rayJSONConfigUsesRealTemplateFile is the same merge-path check for
// the v2ray-json format: the generated outbound must be prepended to the
// template's own outbounds (matching Python's `outbounds +
// json_template["outbounds"]`), and every other key (log/dns/routing/
// inbounds/policy) must survive untouched.
func TestV2rayJSONConfigUsesRealTemplateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v2ray.json")
	content := `{
		"log": {"loglevel": "warning"},
		"dns": {"servers": ["1.1.1.1"]},
		"outbounds": [
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "block", "protocol": "blackhole"}
		]
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	template, ok := LoadJSONTemplate(path)
	if !ok {
		t.Fatal("LoadJSONTemplate returned ok=false for a real file")
	}

	in := EffectiveInbound{Network: "tcp", Port: 443, Security: "tls", SNI: "example.com"}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "uuid-1"}}
	cfg, err := V2rayJSONConfig("My Node", "1.2.3.4", in, settings, template)
	if err != nil {
		t.Fatalf("V2rayJSONConfig: %v", err)
	}

	if _, ok := cfg["log"]; !ok {
		t.Errorf("log block from the template was not preserved: %+v", cfg)
	}
	if _, ok := cfg["dns"]; !ok {
		t.Errorf("dns block from the template was not preserved: %+v", cfg)
	}
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok || len(outbounds) != 3 {
		t.Fatalf("expected 3 outbounds (generated proxy + template's direct + block), got %+v", cfg["outbounds"])
	}
	proxy := outbounds[0].(map[string]any)
	if proxy["protocol"] != "vless" {
		t.Errorf("first outbound must be the generated proxy, got %+v", proxy)
	}
	direct := outbounds[1].(map[string]any)
	if direct["tag"] != "direct" {
		t.Errorf("template's own outbounds must follow the generated proxy, got %+v", outbounds[1])
	}
}
