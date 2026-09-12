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
		//
		// Values match mux/default.json's own "clash" entry exactly (this
		// used to invent {enabled:true, protocol:"smux", max-streams:8} -
		// none of those three values are real: the shipped default is
		// enabled:false, protocol is "h2mux" not "smux", and the key is
		// max_streams with an underscore, not a hyphen). The block is
		// added whenever mux_enable is set regardless of that "enabled"
		// value - matching Python's own unconditional `if mux_enable:
		// node["smux"] = mux_config` - an operator who edits the template
		// file to flip it to true is exactly why this isn't hardcoded to
		// false here.
		node["smux"] = map[string]any{"enabled": false, "protocol": "h2mux", "max_streams": 8}
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
// templatePath, when non-empty and readable, points at a real YAML file
// (CLASH_SUBSCRIPTION_TEMPLATE) providing everything the operator wants
// beyond bare connectivity - mixed-port/dns/tun/sniffer settings, real
// rule-providers, a branded proxy-group name, a full routing rule set.
// Every proxy-group in that file gets its "proxies" list replaced with
// the real, current server names; nothing else in the file is touched.
// With no template configured (or one that fails to load), this falls
// back to the minimum that makes the profile actually route traffic at
// all: one generic "PROXY" select group and a MATCH catch-all.
func ClashConfig(proxies []map[string]any, templatePath string) ([]byte, error) {
	names := make([]string, 0, len(proxies))
	for _, p := range proxies {
		if name, ok := p["name"].(string); ok {
			names = append(names, name)
		}
	}

	if tmpl, ok := LoadYAMLTemplate(templatePath); ok {
		doc := shallowCopyMap(tmpl)
		doc["proxies"] = proxies
		if groups, ok := doc["proxy-groups"].([]any); ok {
			filled := make([]any, len(groups))
			for i, g := range groups {
				gm, ok := g.(map[string]any)
				if !ok {
					filled[i] = g
					continue
				}
				ng := shallowCopyMap(gm)
				ng["proxies"] = names
				filled[i] = ng
			}
			doc["proxy-groups"] = filled
		}
		return yaml.Marshal(doc)
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
