package subscription

import (
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

// ClashProxy builds one Clash (or Clash Meta, with isMeta=true) proxy node,
// porting app/subscription/clash.py's ClashConfiguration/ClashMetaConfiguration
// .make_node/.add. Returns nil, nil for a combination Clash can't represent -
// matching the Python original's silent exclusion, not an error: kcp/
// splithttp/xhttp always (no Clash transport maps to them), plain vless on
// non-meta Clash (the base protocol has no VLESS support at all), and reality
// security on non-meta Clash (no reality-opts field exists there).
func ClashProxy(remark, address string, in EffectiveInbound, settings proxysettings.Settings, isMeta bool) (map[string]any, error) {
	switch in.Network {
	case "kcp", "splithttp", "xhttp":
		return nil, nil
	}
	if !isMeta && in.Security == "reality" {
		return nil, nil
	}
	if !isMeta && settings.Type == proxysettings.VLESS {
		return nil, nil
	}

	node := map[string]any{
		"name": remark, "server": address, "port": in.Port, "udp": true,
	}

	switch settings.Type {
	case proxysettings.VMess:
		node["type"] = "vmess"
		node["uuid"] = settings.VMess.ID
		node["alterId"] = 0
		node["cipher"] = "auto"
	case proxysettings.VLESS:
		node["type"] = "vless"
		node["uuid"] = settings.VLESS.ID
		if settings.VLESS.Flow != "" && (in.Network == "tcp" || in.Network == "raw" || in.Network == "kcp") &&
			in.HeaderType != "http" && in.Security != "none" {
			node["flow"] = string(settings.VLESS.Flow)
		}
	case proxysettings.Trojan:
		node["type"] = "trojan"
		node["password"] = settings.Trojan.Password
	case proxysettings.Shadowsocks:
		// Clash's shadowsocks node is otherwise plain server/port/password/
		// cipher - no network/tls block at all, matching the Python
		// original's own early return right after setting these two fields.
		node["type"] = "ss"
		node["password"] = settings.Shadowsocks.Password
		node["cipher"] = string(settings.Shadowsocks.Method)
		return node, nil
	default:
		return nil, nil
	}

	clashNetwork := in.Network
	switch {
	case in.Network == "http" || in.Network == "h2":
		clashNetwork = "h2"
	case (in.Network == "tcp" || in.Network == "raw") && in.HeaderType == "http":
		clashNetwork = "http"
	case in.Network == "httpupgrade":
		clashNetwork = "ws"
	case in.Network == "tcp" || in.Network == "raw":
		clashNetwork = "tcp"
	}
	node["network"] = clashNetwork

	if in.Security == "tls" || in.Security == "reality" {
		node["tls"] = true
		if settings.Type == proxysettings.Trojan {
			node["sni"] = in.SNI
		} else {
			node["servername"] = in.SNI
		}
		if in.ALPN != "" {
			node["alpn"] = strings.Split(in.ALPN, ",")
		}
		if in.AllowInsecure {
			node["skip-cert-verify"] = true
		}
		if isMeta {
			if in.Fingerprint != "" {
				node["client-fingerprint"] = in.Fingerprint
			}
			if in.Security == "reality" && in.RealityPublicKey != "" {
				node["reality-opts"] = map[string]any{"public-key": in.RealityPublicKey, "short-id": in.RealityShortID}
			}
		}
	}

	switch clashNetwork {
	case "ws":
		opts := map[string]any{}
		if in.Path != "" {
			opts["path"] = in.Path
		}
		if in.HostHeader != "" {
			opts["headers"] = map[string]any{"Host": in.HostHeader}
		}
		if in.Network == "httpupgrade" {
			opts["v2ray-http-upgrade"] = true
			opts["v2ray-http-upgrade-fast-open"] = true
		}
		node["ws-opts"] = opts
	case "grpc":
		opts := map[string]any{}
		if in.Path != "" {
			opts["grpc-service-name"] = in.Path
		}
		node["grpc-opts"] = opts
	case "h2":
		opts := map[string]any{}
		if in.Path != "" {
			opts["path"] = in.Path
		}
		if in.HostHeader != "" {
			opts["host"] = []string{in.HostHeader}
		}
		node["h2-opts"] = opts
	case "http":
		opts := map[string]any{}
		if in.Path != "" {
			opts["path"] = []string{in.Path}
		}
		if in.HostHeader != "" {
			opts["Host"] = in.HostHeader
		}
		node["http-opts"] = opts
	case "tcp":
		opts := map[string]any{}
		if in.Path != "" {
			opts["path"] = []string{in.Path}
		}
		if in.HostHeader != "" {
			opts["headers"] = map[string]any{"Host": in.HostHeader}
		}
		node["tcp-opts"] = opts
	}

	if isMeta && in.MuxEnable && node["flow"] == nil {
		// Vision (a set "flow") can't be multiplexed - the server rejects the
		// request - so a flow'd VLESS node skips smux entirely, matching
		// ClashMetaConfiguration.add's own explicit pop of a would-be smux
		// block once flow is known.
		node["smux"] = map[string]any{"enabled": true, "protocol": "smux", "max-streams": 8}
	}

	return node, nil
}

// ClashConfig renders the full YAML document.
//
// This used to ship "proxies" with a bare, empty "proxy-groups"/"rules"
// (see git history) - reading Python's ClashConfiguration.__init__, which
// starts from that exact same empty skeleton, and assuming that was the
// final shape. It isn't: Python's .render() renders that skeleton through
// a Jinja2 template (config.py's CLASH_SUBSCRIPTION_TEMPLATE, defaulting
// to app/templates/clash/default.yml) that adds a real "select"
// proxy-group listing every proxy_remark and a routing rule set ending in
// a catch-all MATCH - the empty skeleton alone was never what real
// clients received. Without them, Clash/Clash Meta clients (this was
// found via a real one, FlClash) have proxies to look at but nothing
// selecting or routing through any of them - every connection fails,
// which is indistinguishable from "the network is down" to the person
// using the app.
//
// This port keeps it to the minimum that makes the profile actually
// route traffic - one "PROXY" select group and a MATCH catch-all - rather
// than also porting the specific operator's IR rule-provider set and
// "Net Shield" branding baked into that one template file, which belongs
// in a per-installation customization mechanism (not yet built), not the
// shared default every rapido-go install gets.
func ClashConfig(proxies []map[string]any) ([]byte, error) {
	names := make([]string, 0, len(proxies))
	for _, p := range proxies {
		if name, ok := p["name"].(string); ok {
			names = append(names, name)
		}
	}
	doc := map[string]any{
		"proxies": proxies,
		"proxy-groups": []any{
			map[string]any{"name": "PROXY", "type": "select", "proxies": names},
		},
		"rules": []any{"MATCH,PROXY"},
	}
	return yaml.Marshal(doc)
}
