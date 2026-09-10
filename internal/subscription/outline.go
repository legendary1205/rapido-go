package subscription

import (
	"encoding/json"
	"strconv"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

// outlineServer is one entry in a SIP008 "servers" array
// (https://github.com/shadowsocks/shadowsocksr/... the format every real
// Outline client actually parses).
type outlineServer struct {
	ID         string `json:"id"`
	Remarks    string `json:"remarks"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
	Password   string `json:"password"`
	Method     string `json:"method"`
}

// OutlineServer builds one SIP008 server entry for a shadowsocks proxy+host.
// Returns nil, nil for any other protocol - Outline only ever understood
// shadowsocks, matching app/subscription/outline.py's own early return.
func OutlineServer(id, remark, address string, in EffectiveInbound, settings proxysettings.Settings) (any, error) {
	if settings.Type != proxysettings.Shadowsocks {
		return nil, nil
	}
	return outlineServer{
		ID: id, Remarks: remark, Server: address, ServerPort: in.Port,
		Password: settings.Shadowsocks.Password, Method: string(settings.Shadowsocks.Method),
	}, nil
}

// OutlineConfig renders a real SIP008 document: {"version":1,"servers":[...]}.
// This intentionally does NOT reproduce app/subscription/outline.py's own
// bug - that Python class calls dict.update() on one flat dict per host, so
// a user with more than one shadowsocks host silently loses every server but
// the last (see the Go rewrite's own Phase 4 research note on this).
func OutlineConfig(servers []any) ([]byte, error) {
	doc := map[string]any{"version": 1, "servers": servers}
	return json.MarshalIndent(doc, "", "  ")
}

// OutlineServerID derives a per-host id (SIP008 requires one, unique within
// the servers array) from its 0-based position in the walk - simpler than
// hashing the tag, and correctly unique even when the same inbound tag
// serves several hosts differing only by port.
func OutlineServerID(index int) string {
	return strconv.Itoa(index + 1)
}
