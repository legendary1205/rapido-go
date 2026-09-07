package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// sampleXrayConfigJSON is a synthetic (never real) Xray config: one
// two-port inbound with a fake self-signed test cert (reusing
// testCertPEM/testKeyPEM), one single-port inbound, a freedom outbound
// bound to an interface, a blackhole outbound, and routing rules
// referencing both split ports and a whole tag - enough to exercise the
// same shape the real production config the user shared has, without any
// real certificate/customer material.
func sampleXrayConfigJSON(t *testing.T) string {
	t.Helper()
	certLines, _ := json.Marshal(splitPEMLines(testCertPEM))
	keyLines, _ := json.Marshal(splitPEMLines(testKeyPEM))
	return `{
  "log": {"loglevel": "warning"},
  "dns": {"servers": ["9.9.9.10"]},
  "inbounds": [
    {
      "tag": "impnode1", "port": "21000,21004", "protocol": "vless",
      "settings": {"clients": [], "decryption": "none"},
      "streamSettings": {
        "network": "tcp", "security": "tls",
        "tlsSettings": {"serverName": "example.test", "certificates": [{"certificate": ` + string(certLines) + `, "key": ` + string(keyLines) + `}]}
      }
    },
    {
      "tag": "impnode2", "port": 21001, "protocol": "vless",
      "settings": {"clients": [], "decryption": "none"},
      "streamSettings": {"network": "tcp", "security": "none"}
    }
  ],
  "outbounds": [
    {"tag": "imp-germany", "protocol": "freedom", "streamSettings": {"sockopt": {"interface": "germany"}}},
    {"protocol": "blackhole", "tag": "imp-blackhole"}
  ],
  "routing": {
    "rules": [
      {"type": "field", "inboundTag": ["impnode1"], "localPort": "21000", "outboundTag": "imp-germany"},
      {"type": "field", "inboundTag": ["impnode1"], "outboundTag": "imp-blackhole"}
    ]
  }
}`
}

// splitPEMLines turns a PEM block (testCertPEM/testKeyPEM, each with a
// trailing newline) into the line-array shape Xray's own config format
// uses for a certificate/key - trims the trailing empty element
// strings.Split leaves behind after the final "\n".
func splitPEMLines(pem string) []string {
	lines := strings.Split(pem, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func TestImportXrayConfigPreviewDoesNotWriteAnything(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "POST", "/api/inbounds/import-xray", token, map[string]interface{}{
		"config": sampleXrayConfigJSON(t), "confirm": false,
	})
	if resp.Code != 200 {
		t.Fatalf("preview import: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["applied"] != false {
		t.Errorf("applied = %v, want false for a preview", resp.Body["applied"])
	}
	if got := resp.Body["inbounds_created"].(float64); got != 3 {
		t.Errorf("inbounds_created (preview count) = %v, want 3 (impnode1 splits into 2, plus impnode2)", got)
	}

	detail := doRequest(t, router, "GET", "/api/inbounds/detail", token, nil)
	var rows []map[string]interface{}
	if err := json.Unmarshal(detail.Raw, &rows); err != nil {
		t.Fatalf("decode inbounds detail: %v", err)
	}
	for _, r := range rows {
		if tag, _ := r["tag"].(string); tag == "impnode1-21000" || tag == "impnode2" {
			t.Fatalf("preview (confirm:false) must not write anything, but found %v in the real inbounds list", r["tag"])
		}
	}
}

func TestImportXrayConfigConfirmActuallyCreatesEverything(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "POST", "/api/inbounds/import-xray", token, map[string]interface{}{
		"config": sampleXrayConfigJSON(t), "confirm": true,
	})
	if resp.Code != 200 {
		t.Fatalf("confirm import: %d %v", resp.Code, resp.Body)
	}
	if resp.Body["applied"] != true {
		t.Errorf("applied = %v, want true", resp.Body["applied"])
	}

	detailResp := doRequest(t, router, "GET", "/api/inbounds/detail", token, nil)
	var rows []map[string]interface{}
	if err := json.Unmarshal(detailResp.Raw, &rows); err != nil {
		t.Fatalf("decode inbounds detail: %v", err)
	}
	byTag := map[string]map[string]interface{}{}
	for _, r := range rows {
		byTag[r["tag"].(string)] = r
	}
	for _, wantTag := range []string{"impnode1-21000", "impnode1-21004", "impnode2"} {
		if _, ok := byTag[wantTag]; !ok {
			t.Errorf("expected inbound %q to exist after a confirmed import, got tags %v", wantTag, keysOf(byTag))
		}
	}
	// The parser joins Xray's line-array certificate with "\n" (no
	// trailing newline after the last line) - testCertPEM itself carries
	// one trailing newline as a Go source-formatting convenience, so the
	// stored value is one byte shorter than the raw constant.
	wantCert := strings.TrimSuffix(testCertPEM, "\n")
	if byTag["impnode1-21000"]["security"] != "tls" || byTag["impnode1-21000"]["tls_certificate"] != wantCert {
		t.Errorf("impnode1-21000 tls_certificate = %q, want %q", byTag["impnode1-21000"]["tls_certificate"], wantCert)
	}

	hostsResp := doRequest(t, router, "GET", "/api/hosts", token, nil)
	hosts, ok := hostsResp.Body["impnode1-21000"].([]interface{})
	if !ok || len(hosts) != 1 {
		t.Fatalf("expected exactly one host under impnode1-21000, got %v", hostsResp.Body["impnode1-21000"])
	}
	host := hosts[0].(map[string]interface{})
	if port, ok := host["port"].(float64); !ok || int(port) != 21000 {
		t.Errorf("imported host port = %v, want 21000 (not the placeholder-empty default)", host["port"])
	}

	coreResp := doRequest(t, router, "GET", "/api/settings/core-config", token, nil)
	outbounds := coreResp.Body["outbounds"].([]interface{})
	var germany map[string]interface{}
	for _, ob := range outbounds {
		m := ob.(map[string]interface{})
		if m["tag"] == "imp-germany" {
			germany = m
		}
	}
	if germany == nil || germany["bind_interface"] != "germany" {
		t.Errorf("expected an imported outbound imp-germany with bind_interface=germany, got %v", outbounds)
	}
	rules := coreResp.Body["routing_rules"].([]interface{})
	if len(rules) != 2 {
		t.Fatalf("routing_rules = %v, want 2", rules)
	}
	rule1 := rules[0].(map[string]interface{})
	inbound1 := rule1["inbound"].([]interface{})
	if len(inbound1) != 1 || inbound1[0] != "impnode1-21000" {
		t.Errorf("rule 1 inbound = %v, want [impnode1-21000]", inbound1)
	}
}

func TestImportXrayConfigMergesRatherThanReplacesExistingCoreConfig(t *testing.T) {
	router, token := newTestRouter(t)
	// A pre-existing custom outbound the import must not wipe.
	doRequest(t, router, "PUT", "/api/settings/core-config", token, map[string]interface{}{
		"outbounds": []map[string]interface{}{{"tag": "pre-existing", "type": "direct"}},
	})

	resp := doRequest(t, router, "POST", "/api/inbounds/import-xray", token, map[string]interface{}{
		"config": sampleXrayConfigJSON(t), "confirm": true,
	})
	if resp.Code != 200 {
		t.Fatalf("confirm import: %d %v", resp.Code, resp.Body)
	}

	coreResp := doRequest(t, router, "GET", "/api/settings/core-config", token, nil)
	outbounds := coreResp.Body["outbounds"].([]interface{})
	tags := map[string]bool{}
	for _, ob := range outbounds {
		tags[ob.(map[string]interface{})["tag"].(string)] = true
	}
	if !tags["pre-existing"] {
		t.Errorf("expected the pre-existing outbound to survive the import, got tags %v", tags)
	}
	if !tags["imp-germany"] {
		t.Errorf("expected the imported outbound to be added too, got tags %v", tags)
	}
}

func TestImportXrayConfigRejectsInvalidJSON(t *testing.T) {
	router, token := newTestRouter(t)
	resp := doRequest(t, router, "POST", "/api/inbounds/import-xray", token, map[string]interface{}{
		"config": "not a real config", "confirm": false,
	})
	if resp.Code != 422 {
		t.Errorf("invalid config: got %d, want 422 (%v)", resp.Code, resp.Body)
	}
}

func TestImportXrayConfigIsSudoOnly(t *testing.T) {
	router, sudoToken := newTestRouter(t)
	doRequest(t, router, "POST", "/api/admin", sudoToken, map[string]interface{}{"username": "xrayimport-owner", "password": "pw12345", "is_sudo": false})
	nonSudo := loginAs(t, router, "xrayimport-owner", "pw12345")

	resp := doRequest(t, router, "POST", "/api/inbounds/import-xray", nonSudo, map[string]interface{}{"config": "{}", "confirm": false})
	if resp.Code != 403 {
		t.Errorf("import as non-sudo: got %d, want 403 (%v)", resp.Code, resp.Body)
	}
}

func keysOf(m map[string]map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
