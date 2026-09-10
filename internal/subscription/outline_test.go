package subscription

import (
	"encoding/json"
	"testing"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

func TestOutlineServerOnlyShadowsocks(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 443}
	settings := proxysettings.Settings{Type: proxysettings.VLESS, VLESS: &proxysettings.VLESSSettings{ID: "u"}}

	server, err := OutlineServer("1", "t", "1.2.3.4", in, settings)
	if err != nil {
		t.Fatalf("OutlineServer: %v", err)
	}
	if server != nil {
		t.Errorf("expected nil for a non-shadowsocks proxy, got %+v", server)
	}
}

func TestOutlineConfigIsRealSIP008(t *testing.T) {
	in := EffectiveInbound{Network: "tcp", Port: 8388}
	settings := proxysettings.Settings{Type: proxysettings.Shadowsocks, Shadowsocks: &proxysettings.ShadowsocksSettings{Password: "pw", Method: "aes-256-gcm"}}

	s1, _ := OutlineServer(OutlineServerID(0), "First", "1.1.1.1", in, settings)
	s2, _ := OutlineServer(OutlineServerID(1), "Second", "2.2.2.2", in, settings)
	raw, err := OutlineConfig([]any{s1, s2})
	if err != nil {
		t.Fatalf("OutlineConfig: %v", err)
	}

	var doc struct {
		Version int `json:"version"`
		Servers []struct {
			ID         string `json:"id"`
			Remarks    string `json:"remarks"`
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
			Password   string `json:"password"`
			Method     string `json:"method"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	if doc.Version != 1 {
		t.Errorf("version = %d, want 1 (SIP008 requires this field)", doc.Version)
	}
	if len(doc.Servers) != 2 {
		t.Fatalf("servers = %d, want 2 - both must survive, not just the last (the known Python bug this port fixes)", len(doc.Servers))
	}
	if doc.Servers[0].ID == doc.Servers[1].ID {
		t.Errorf("both servers got the same id %q - SIP008 requires unique ids", doc.Servers[0].ID)
	}
	if doc.Servers[0].Remarks != "First" || doc.Servers[1].Remarks != "Second" {
		t.Errorf("remarks wrong or out of order: %+v", doc.Servers)
	}
}
