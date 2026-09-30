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

// SingBoxConfig renders the full document: every generated outbound, a
// "selector" outbound listing all of them as a convenience default, and
// enough of a real config around them (a "direct" outbound, a tun inbound,
// DNS, and a default route sending everything through the selector) that
// the document is actually usable standalone by an app that loads it as a
// complete profile rather than merging it into a config of its own - the
// official sing-box app in particular does the former: given only the
// outbounds this function used to emit on their own, it dials each one
// successfully for its own latency probe (which needs no local inbound or
// route at all) but never actually intercepts the device's traffic, so
// every real connection still leaves over the un-proxied network path -
// this is what a report of "shows as connected/pingable but my IP never
// changes" means. The DNS/route/tun shape below is deliberately minimal
// (bypass private IPs, everything else through the proxy) - no
// geosite/geoip ad-block rule sets, which would need this project to host
// and version its own rule-set files; an admin wanting that can still layer
// it on by importing this same outbounds list into their own richer
// profile.
func SingBoxConfig(outbounds []map[string]any) ([]byte, error) {
	tags := make([]string, 0, len(outbounds))
	for _, o := range outbounds {
		tags = append(tags, o["tag"].(string))
	}
	allOutbounds := append(append([]map[string]any{}, outbounds...),
		map[string]any{"type": "selector", "tag": "proxy", "outbounds": tags, "default": firstOrEmpty(tags)},
		map[string]any{"type": "direct", "tag": "direct"},
	)
	doc := map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": map[string]any{
			"servers": []map[string]any{
				{"tag": "dns-remote", "type": "https", "server": "8.8.8.8", "detour": "proxy"},
				{"tag": "dns-direct", "type": "local", "detour": "direct"},
			},
			"rules": []map[string]any{
				{"ip_is_private": true, "server": "dns-direct"},
			},
			"final": "dns-remote",
		},
		"inbounds": []map[string]any{
			{
				"type": "tun", "tag": "tun-in",
				"address":      []string{"172.19.0.1/28", "fdfe:dcba:9876::1/126"},
				"auto_route":   true,
				"strict_route": true,
				"stack":        "mixed",
			},
		},
		// Sniffing used to be a plain bool on the inbound itself
		// (InboundOptions.SniffEnabled) - sing-box 1.11 deprecated that field
		// in favor of a route rule/action, and 1.13 removed it outright, so
		// setting it on the tun inbound above makes a current client reject
		// the whole document at load time ("legacy inbound fields ... removed
		// in sing-box 1.13.0"). This rule is that migration's replacement,
		// same shape a real reference config uses.
		"route": map[string]any{
			"auto_detect_interface": true,
			// Every host's address here is a domain (the marketing hostnames
			// hosts.address holds, not raw IPs), so every outbound above is a
			// dial with a domain to resolve first. sing-box 1.12 deprecated
			// resolving that implicitly through whatever bootstrap DNS the
			// core happened to have and now wants a resolver named
			// explicitly - a route-level default covers every such dial at
			// once instead of repeating a domain_resolver field on each of
			// the 15 outbounds above. Pointed at dns-direct (not
			// dns-remote): resolving the proxy's own address through the
			// proxy would be circular.
			"default_domain_resolver": "dns-direct",
			"rules": []map[string]any{
				{"inbound": "tun-in", "action": "sniff", "timeout": "1s"},
				{"protocol": "dns", "action": "hijack-dns"},
				{"ip_is_private": true, "outbound": "direct"},
			},
			"final": "proxy",
		},
		"outbounds": allOutbounds,
	}
	return json.MarshalIndent(doc, "", "  ")
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
