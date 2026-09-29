package subscription

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

// SingBoxOutbound builds one sing-box outbound object for a proxy+host,
// porting app/subscription/singbox.py's SingBoxConfiguration.add. Returns
// nil, nil for network types sing-box's outbound side can't represent from
// this data (kcp, splithttp/xhttp, quic with a header type) - the caller
// should simply skip those hosts, matching the Python original's silent
// exclusion.
func SingBoxOutbound(tag, address string, in EffectiveInbound, settings proxysettings.Settings) (map[string]any, error) {
	switch in.Network {
	case "kcp", "splithttp", "xhttp":
		return nil, nil
	}

	out := map[string]any{
		"type": string(settings.Type), "tag": tag, "server": address, "server_port": in.Port,
	}

	switch settings.Type {
	case proxysettings.VMess:
		out["uuid"] = settings.VMess.ID
	case proxysettings.VLESS:
		out["uuid"] = settings.VLESS.ID
		if settings.VLESS.Flow != "" && (in.Network == "tcp" || in.Network == "raw") {
			out["flow"] = string(settings.VLESS.Flow)
		}
	case proxysettings.Trojan:
		out["password"] = settings.Trojan.Password
	case proxysettings.Shadowsocks:
		out["password"] = settings.Shadowsocks.Password
		out["method"] = string(settings.Shadowsocks.Method)
	case proxysettings.Hysteria2:
		out["password"] = settings.Hysteria2.Password
		if in.UpMbps > 0 {
			out["up_mbps"] = in.UpMbps
		}
		if in.DownMbps > 0 {
			out["down_mbps"] = in.DownMbps
		}
		if in.Hysteria2ObfsPassword != "" {
			out["obfs"] = map[string]any{"type": "salamander", "password": in.Hysteria2ObfsPassword}
		}
	case proxysettings.TUIC:
		out["uuid"] = settings.TUIC.ID
		out["password"] = settings.TUIC.Password
		congestionControl := in.CongestionControl
		if congestionControl == "" {
			congestionControl = "cubic" // the node's own default - see internal/nodecore/tuic
		}
		out["congestion_control"] = congestionControl
		if in.ZeroRTTHandshake {
			out["zero_rtt_handshake"] = true
		}
	case proxysettings.Snell:
		out["psk"] = in.SnellPSK
		out["version"] = 6
		out["userkey"] = settings.Snell.UserKey
		if in.SnellV6Mode != "" && in.SnellV6Mode != "default" {
			out["mode"] = in.SnellV6Mode
		}
	case proxysettings.AnyTLS:
		out["password"] = settings.AnyTLS.Password
	default:
		return nil, fmt.Errorf("subscription: unknown proxy type %q", settings.Type)
	}

	if transport := singBoxTransport(in); transport != nil {
		out["transport"] = transport
	}
	if tls := singBoxTLS(in); tls != nil {
		out["tls"] = tls
	}
	// hysteria2/tuic/snell/anytls each have their own reason to have no
	// multiplex field in sing-box's own option structs (hysteria2/tuic:
	// QUIC-based; snell: its own doc comment on internal/nodecore/snell;
	// anytls: option.AnyTLSOutboundOptions has no multiplex field either -
	// its own idle-session pooling is the closest thing it has) - see this
	// function's own doc comment on the QUIC pair's mandatory TLS for why
	// they're otherwise built like every classic TCP-family type above.
	if settings.Type == proxysettings.Hysteria2 || settings.Type == proxysettings.TUIC || settings.Type == proxysettings.Snell || settings.Type == proxysettings.AnyTLS {
		return out, nil
	}
	// Python's SingBoxConfiguration.make_outbound sets this block on EVERY
	// outbound unconditionally (not gated by mux_enable at all), from
	// mux/default.json's own "sing-box" entry - only flipping `enabled`
	// to `mux_enable and not flow` if the template's own `enabled` was
	// already true. The shipped default has it false, so under an
	// unmodified template `mux_enable` never has any observable effect on
	// this format at all - a real, faithfully-preserved quirk, not
	// something to "fix" by making it obey the per-host toggle instead.
	out["multiplex"] = map[string]any{"enabled": false, "protocol": "h2mux", "max_streams": 8}
	return out, nil
}

func singBoxTransport(in EffectiveInbound) map[string]any {
	switch in.Network {
	case "ws":
		t := map[string]any{"type": "ws", "path": in.Path}
		if in.HostHeader != "" {
			t["headers"] = map[string]any{"Host": in.HostHeader}
		}
		return t
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": in.Path}
	case "http", "h2":
		return map[string]any{"type": "http", "path": in.Path, "host": []string{in.HostHeader}}
	case "httpupgrade":
		return map[string]any{"type": "httpupgrade", "path": in.Path, "host": in.HostHeader}
	default: // tcp/raw and anything else needs no transport block (raw TCP)
		return nil
	}
}

func singBoxTLS(in EffectiveInbound) map[string]any {
	switch in.Security {
	case "tls":
		tls := map[string]any{"enabled": true, "server_name": in.SNI}
		if in.AllowInsecure {
			tls["insecure"] = true
		}
		if in.ALPN != "" {
			tls["alpn"] = strings.Split(in.ALPN, ",")
		}
		if in.Fingerprint != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": in.Fingerprint}
		}
		return tls
	case "reality":
		return map[string]any{
			"enabled": true, "server_name": in.SNI,
			"utls":    map[string]any{"enabled": true, "fingerprint": in.Fingerprint},
			"reality": map[string]any{"enabled": true, "public_key": in.RealityPublicKey, "short_id": in.RealityShortID},
		}
	default:
		return nil
	}
}

// SingBoxConfig renders the full document: a minimal skeleton plus every
// generated outbound, with a "selector" outbound listing all of them as a
// convenience default. Deliberately simpler than the current Python
// system's default.json base template (no TUN inbound, no DNS/routing
// rule-set boilerplate) - that base is generic app configuration unrelated
// to per-user subscription data and can be layered on separately without
// touching this generation logic.
func SingBoxConfig(outbounds []map[string]any) ([]byte, error) {
	tags := make([]string, 0, len(outbounds))
	for _, o := range outbounds {
		tags = append(tags, o["tag"].(string))
	}
	doc := map[string]any{
		"log": map[string]any{"level": "warn"},
		"outbounds": append(append([]map[string]any{}, outbounds...), map[string]any{
			"type": "selector", "tag": "proxy", "outbounds": tags, "default": firstOrEmpty(tags),
		}),
	}
	return json.MarshalIndent(doc, "", "  ")
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
