package subscription

import (
	"encoding/json"
	"strings"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

// v2rayJSONEmail is the fixed "email" field every v2ray JSON outbound user
// object carries - not a real address, just an identifying string real
// clients display in their server list. Matches the convention
// app/subscription/v2ray.py's V2rayJsonConfig already settled on
// (github.com/legendary1205/rapido), not a per-user value.
const v2rayJSONEmail = "https://github.com/legendary1205/rapido"

// V2rayJSONConfig builds one full, standalone v2ray-core client config for a
// proxy+host - app/subscription/v2ray.py's V2rayJsonConfig produces a JSON
// ARRAY of these, one per host, and a real client (V2rayNG-family) imports
// each array element as its own separate server profile; render this same
// shape for one host, the caller assembles the array. Returns nil, nil for a
// network/settings combination this can't represent - kcp/splithttp/xhttp,
// same exclusion SingBoxOutbound already applies, since rapido-go's own
// EffectiveInbound has no fields to describe those transports' settings at
// all (no seed/scMaxEachPostBytes/etc - see effective.go).
//
// template, when non-nil (loaded once by the caller via LoadJSONTemplate
// from V2RAY_SUBSCRIPTION_TEMPLATE), is a real standalone xray-core config
// - log/dns/routing/policy/local inbounds/its own outbounds list - that
// this proxy's outbound gets prepended onto, matching Python's add_config
// (`outbounds + json_template["outbounds"]`): every generated profile in
// the subscription carries the operator's full routing/ad-block setup,
// not just the bare proxy. With no template configured (or one that
// fails to load), this falls back to the minimum viable shape: the proxy
// outbound plus a direct/blackhole pair, no curated random-user-agent
// list (EffectiveInbound.RandomUserAgent exists but isn't acted on here
// either - the same scope cut SingBoxConfig's own doc comment commits to,
// applied consistently for this format's fallback).
func V2rayJSONConfig(remark, address string, in EffectiveInbound, settings proxysettings.Settings, template map[string]any) (map[string]any, error) {
	switch in.Network {
	case "kcp", "splithttp", "xhttp":
		return nil, nil
	}

	var vlessFlowSet bool
	outbound := map[string]any{"tag": "proxy", "protocol": string(settings.Type)}
	switch settings.Type {
	case proxysettings.VMess:
		outbound["settings"] = map[string]any{
			"vnext": []map[string]any{{
				"address": address, "port": in.Port,
				"users": []map[string]any{{"id": settings.VMess.ID, "alterId": 0, "email": v2rayJSONEmail, "security": "auto"}},
			}},
		}
	case proxysettings.VLESS:
		user := map[string]any{"id": settings.VLESS.ID, "encryption": "none", "email": v2rayJSONEmail}
		if settings.VLESS.Flow != "" && (in.Network == "tcp" || in.Network == "raw") &&
			in.HeaderType != "http" && (in.Security == "tls" || in.Security == "reality") {
			user["flow"] = string(settings.VLESS.Flow)
			vlessFlowSet = true
		}
		outbound["settings"] = map[string]any{
			"vnext": []map[string]any{{"address": address, "port": in.Port, "users": []map[string]any{user}}},
		}
	case proxysettings.Trojan:
		outbound["settings"] = map[string]any{
			"servers": []map[string]any{{"address": address, "port": in.Port, "password": settings.Trojan.Password, "email": v2rayJSONEmail}},
		}
	case proxysettings.Shadowsocks:
		outbound["settings"] = map[string]any{
			"servers": []map[string]any{{
				"address": address, "port": in.Port, "password": settings.Shadowsocks.Password,
				"email": v2rayJSONEmail, "method": string(settings.Shadowsocks.Method), "uot": false,
			}},
		}
	default:
		return nil, nil
	}

	stream := map[string]any{"network": in.Network}
	if transport := v2rayJSONTransport(in); transport != nil {
		stream[in.Network+"Settings"] = transport
	}
	if tls := v2rayJSONTLS(in); tls != nil {
		security := in.Security
		stream["security"] = security
		stream[security+"Settings"] = tls
	}
	outbound["streamSettings"] = stream

	if in.MuxEnable && !vlessFlowSet {
		// Vision (a set flow) can't be multiplexed - the server rejects the
		// request - same guard clash.go's ClashProxy and Python's own
		// V2rayJsonConfig.add apply (see that function's own "uses_vision"
		// comment on why: only the JSON formats carry mux at all, a vless://
		// share link never does, which is why the same host works when added
		// by hand but not through a mux-enabled JSON subscription).
		//
		// xudpProxyUDP443 deliberately omitted: mux/default.json's real
		// "v2ray" entry does carry it, but it's a newer Xray-core Mux field
		// - a real client (v2box) rejected the whole config outright with
		// `infra/conf: unknown "xudpProxyUDP443"` the moment it appeared,
		// because its bundled core predates the field. Losing this one
		// UDP-over-port-443 tuning knob on older clients is a far smaller
		// cost than every proxy on the subscription refusing to parse.
		outbound["mux"] = map[string]any{"enabled": true, "concurrency": 8, "xudpConcurrency": 8}
	}

	if template != nil {
		doc := shallowCopyMap(template)
		doc["remarks"] = remark
		base, _ := template["outbounds"].([]any)
		outbounds := make([]any, 0, len(base)+1)
		outbounds = append(outbounds, outbound)
		outbounds = append(outbounds, base...)
		doc["outbounds"] = outbounds
		return doc, nil
	}

	config := map[string]any{
		"remarks": remark,
		"outbounds": []map[string]any{
			outbound,
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "block", "protocol": "blackhole"},
		},
	}
	return config, nil
}

func v2rayJSONTransport(in EffectiveInbound) map[string]any {
	switch in.Network {
	case "ws":
		t := map[string]any{}
		if in.Path != "" {
			t["path"] = in.Path
		}
		if in.HostHeader != "" {
			t["headers"] = map[string]any{"Host": in.HostHeader}
		}
		return t
	case "grpc":
		t := map[string]any{"multiMode": false}
		if in.Path != "" {
			t["serviceName"] = in.Path
		}
		if in.HostHeader != "" {
			t["authority"] = in.HostHeader
		}
		return t
	case "http", "h2":
		t := map[string]any{"path": in.Path}
		if in.HostHeader != "" {
			t["host"] = []string{in.HostHeader}
		} else {
			t["host"] = []string{}
		}
		return t
	case "httpupgrade":
		t := map[string]any{}
		if in.Path != "" {
			t["path"] = in.Path
		}
		if in.HostHeader != "" {
			t["host"] = in.HostHeader
		}
		return t
	default: // tcp/raw needs no per-network settings block at all
		return nil
	}
}

func v2rayJSONTLS(in EffectiveInbound) map[string]any {
	switch in.Security {
	case "tls":
		t := map[string]any{}
		if in.SNI != "" {
			t["serverName"] = in.SNI
		}
		if in.AllowInsecure {
			t["allowInsecure"] = true
		}
		if in.Fingerprint != "" {
			t["fingerprint"] = in.Fingerprint
		}
		if in.ALPN != "" {
			t["alpn"] = strings.Split(in.ALPN, ",")
		}
		return t
	case "reality":
		t := map[string]any{"show": false}
		if in.SNI != "" {
			t["serverName"] = in.SNI
		}
		if in.Fingerprint != "" {
			t["fingerprint"] = in.Fingerprint
		}
		if in.RealityPublicKey != "" {
			t["publicKey"] = in.RealityPublicKey
		}
		if in.RealityShortID != "" {
			t["shortId"] = in.RealityShortID
		}
		return t
	default:
		return nil
	}
}

// V2rayJSONArray renders the full subscription body: a JSON array of
// independent per-host client configs (see V2rayJSONConfig's own doc
// comment on why this is an array, not one merged document).
func V2rayJSONArray(configs []map[string]any) ([]byte, error) {
	return json.MarshalIndent(configs, "", "  ")
}
