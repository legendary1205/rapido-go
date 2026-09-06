package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
)

// BuildLink returns one vmess://, vless://, trojan:// or ss:// share link
// for the given proxy secret on the given effective inbound - ports
// app/subscription/v2ray.py's V2rayShareLink.add/vmess/vless/trojan/
// shadowsocks. remark should already have its {VAR} placeholders resolved
// (see vars.go).
func BuildLink(remark, address string, in EffectiveInbound, settings proxysettings.Settings) (string, error) {
	switch settings.Type {
	case proxysettings.VMess:
		return vmessLink(remark, address, in, settings.VMess), nil
	case proxysettings.VLESS:
		return vlessLink(remark, address, in, settings.VLESS), nil
	case proxysettings.Trojan:
		return trojanLink(remark, address, in, settings.Trojan), nil
	case proxysettings.Shadowsocks:
		return ssLink(remark, address, in, settings.Shadowsocks), nil
	default:
		return "", fmt.Errorf("subscription: unknown proxy type %q", settings.Type)
	}
}

// tlsQueryParams appends the security-specific query params shared by
// vless/trojan (and mirrored into vmess's JSON separately) - matches the
// `if tls == "tls" / elif tls == "reality"` block repeated identically in
// vless() and trojan() in the current Python code.
func tlsQueryParams(q url.Values, in EffectiveInbound) {
	switch in.Security {
	case "tls":
		q.Set("sni", in.SNI)
		q.Set("fp", in.Fingerprint)
		if in.ALPN != "" {
			q.Set("alpn", in.ALPN)
		}
		if in.FragmentSetting != "" {
			q.Set("fragment", in.FragmentSetting)
		}
		if in.AllowInsecure {
			q.Set("allowInsecure", "1")
		}
	case "reality":
		q.Set("sni", in.SNI)
		q.Set("fp", in.Fingerprint)
		q.Set("pbk", in.RealityPublicKey)
		q.Set("sid", in.RealityShortID)
	}
}

// transportQueryParams appends the network-specific query params shared by
// vless/trojan, matching the net-branch chain in vless()/trojan().
func transportQueryParams(q url.Values, in EffectiveInbound) {
	switch in.Network {
	case "grpc":
		q.Set("serviceName", in.Path)
		q.Set("authority", in.HostHeader)
		q.Set("mode", "gun")
	case "quic":
		q.Set("key", in.Path)
		q.Set("quicSecurity", in.HostHeader)
	case "kcp":
		q.Set("seed", in.Path)
		q.Set("host", in.HostHeader)
	default: // ws, tcp/raw, splithttp/xhttp, http, and anything else fall to path+host
		q.Set("path", in.Path)
		q.Set("host", in.HostHeader)
	}
}

func vlessLink(remark, address string, in EffectiveInbound, s *proxysettings.VLESSSettings) string {
	q := url.Values{"security": {in.Security}, "type": {in.Network}, "headerType": {in.HeaderType}}
	if s.Flow != "" && (in.Security == "tls" || in.Security == "reality") &&
		(in.Network == "tcp" || in.Network == "raw" || in.Network == "kcp") && in.HeaderType != "http" {
		q.Set("flow", string(s.Flow))
	}
	transportQueryParams(q, in)
	tlsQueryParams(q, in)
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", s.ID, address, in.Port, q.Encode(), url.PathEscape(remark))
}

func trojanLink(remark, address string, in EffectiveInbound, s *proxysettings.TrojanSettings) string {
	q := url.Values{"security": {in.Security}, "type": {in.Network}, "headerType": {in.HeaderType}}
	if s.Flow != "" && (in.Security == "tls" || in.Security == "reality") &&
		(in.Network == "tcp" || in.Network == "raw" || in.Network == "kcp") && in.HeaderType != "http" {
		q.Set("flow", string(s.Flow))
	}
	transportQueryParams(q, in)
	tlsQueryParams(q, in)
	return fmt.Sprintf("trojan://%s@%s:%d?%s#%s", quoteKeepColon(s.Password), address, in.Port, q.Encode(), url.PathEscape(remark))
}

// quoteKeepColon matches Python's urlparse.quote(password, safe=':') for
// the trojan:// userinfo segment: every byte outside RFC 3986's unreserved
// set gets percent-encoded EXCEPT ':' - notably including '@', which
// url.PathEscape leaves alone (legal in a path segment) but would be
// ambiguous with the literal '@' separating userinfo from host here.
func quoteKeepColon(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-' || c == '_' || c == '.' || c == '~' || c == ':':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func vmessLink(remark, address string, in EffectiveInbound, s *proxysettings.VMessSettings) string {
	payload := map[string]any{
		"add": address, "aid": "0", "host": in.HostHeader, "id": s.ID, "net": in.Network,
		"path": in.Path, "port": fmt.Sprintf("%d", in.Port), "ps": remark, "scy": "auto",
		"tls": in.Security, "type": in.HeaderType, "v": "2",
	}
	switch in.Network {
	case "grpc":
		payload["mode"] = "gun"
		payload["path"] = in.Path
	case "kcp":
		payload["type"] = in.HeaderType
	}
	if in.FragmentSetting != "" {
		payload["fragment"] = in.FragmentSetting
	}
	switch in.Security {
	case "tls":
		payload["sni"] = in.SNI
		payload["fp"] = in.Fingerprint
		if in.ALPN != "" {
			payload["alpn"] = in.ALPN
		}
		if in.AllowInsecure {
			payload["allowInsecure"] = 1
		}
	case "reality":
		payload["sni"] = in.SNI
		payload["fp"] = in.Fingerprint
		payload["pbk"] = in.RealityPublicKey
		payload["sid"] = in.RealityShortID
	}

	raw, _ := json.Marshal(sortedMap(payload))
	return "vmess://" + base64.StdEncoding.EncodeToString(raw)
}

func ssLink(remark, address string, in EffectiveInbound, s *proxysettings.ShadowsocksSettings) string {
	userinfo := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", s.Method, s.Password)))
	return fmt.Sprintf("ss://%s@%s:%d#%s", userinfo, address, in.Port, url.PathEscape(remark))
}

// sortedMap marshals with keys in sorted order, matching Python's
// json.dumps(payload, sort_keys=True) - real vmess:// clients don't care
// about key order, but this keeps output byte-reproducible for tests.
func sortedMap(m map[string]any) json.RawMessage {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(m[k])
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return json.RawMessage(b.String())
}
