package xrayimport

import "encoding/json"

// ExportInbound is BuildXrayJSON's per-inbound input - the export
// direction's counterpart to Inbound (the import direction's shape).
// They're deliberately separate types: import needs HostALPN/
// HostFingerprint (to synthesize a default host row), export needs
// SNI/Host/Clients (to reconstruct a real inbound's settings/
// streamSettings) - overloading one struct for both would blur which
// fields actually matter on which side.
type ExportInbound struct {
	Tag        string
	Protocol   string
	Network    string
	HeaderType string
	Security   string

	RealityPrivateKey string
	RealityShortIDs   []string
	RealityServerName string
	RealityServerPort int32

	TLSCertificate string
	TLSKey         string
	TLSServerName  string

	Port int32
	// Ports is every port this inbound listens on. With more than one it is
	// what gets exported (Port is then ignored); with zero or one, Port is.
	Ports []int
	SNI   string
	Host  string

	Clients []ExportClient
}

// ExportClient is one user's credentials on one inbound - only the fields
// their protocol actually uses are populated (mirrors
// internal/httpapi/nodeconfig.go's nodeConfigUserSpec, the same per-user
// data already assembled for every real node's own config).
type ExportClient struct {
	Email    string
	UUID     string
	Flow     string
	Password string
	Method   string
}

// BuildXrayJSON renders a real, standard Xray-core config JSON document -
// the inverse of ParseXrayConfig, for the one real-world consumer this
// exists for: GET /api/core/config, the endpoint every external reseller
// bot (confirmed via a real one's public source) reads a panel's live core
// config from.
//
// Scope matches this panel's own real, current fleet exactly: TCP (raw)
// transport with an optional "http" header, tls/reality/none security,
// and vmess/vless/trojan/shadowsocks clients - every inbound this fleet
// actually has. An inbound using any other transport (ws/grpc/h2/etc -
// none exist in production today, and ListAutoSyncInbounds's own query
// doesn't even select the path/host-header columns those would need)
// still gets a syntactically valid entry with its real `network` value
// and no transport-specific settings block, rather than an error - a
// real bot reading this can still see the inbound exists, tag and port
// and protocol correct, even though the connection details for that one
// uncommon case would need a hand edit.
func BuildXrayJSON(inbounds []ExportInbound, outbounds []Outbound, rules []RoutingRule, dnsServers []DNSServer, logLevel string, sniffEnabled bool) ([]byte, error) {
	doc := map[string]any{
		"log": map[string]any{"loglevel": exportLogLevel(logLevel)},
	}

	xInbounds := make([]map[string]any, 0, len(inbounds))
	for _, in := range inbounds {
		xInbounds = append(xInbounds, buildXrayInbound(in, sniffEnabled))
	}
	doc["inbounds"] = xInbounds

	xOutbounds := make([]map[string]any, 0, len(outbounds)+2)
	for _, ob := range outbounds {
		if x := buildXrayOutbound(ob); x != nil {
			xOutbounds = append(xOutbounds, x)
		}
	}
	doc["outbounds"] = xOutbounds

	xRules := make([]map[string]any, 0, len(rules))
	for _, r := range rules {
		xRules = append(xRules, buildXrayRoutingRule(r))
	}
	doc["routing"] = map[string]any{
		// Dropped on import with no equivalent stored (ParseXrayConfig's
		// own warning on cfg.Routing.DomainStrategy) - IPIfNonMatch is
		// Xray's own default value, used here rather than an empty string
		// so a re-imported export is itself a valid, complete config.
		"domainStrategy": "IPIfNonMatch",
		"rules":          xRules,
	}

	xDNS := make([]any, 0, len(dnsServers))
	for _, d := range dnsServers {
		if d.Address != "" {
			xDNS = append(xDNS, d.Address)
		}
	}
	doc["dns"] = map[string]any{"servers": xDNS}

	return json.MarshalIndent(doc, "", "  ")
}

// exportLogLevel is mapLogLevel's inverse (sing-box's levels back to
// Xray's) - "warning" is Xray's own default, used for anything sing-box
// has that Xray doesn't (trace/fatal/panic).
func exportLogLevel(level string) string {
	switch level {
	case "debug", "info", "error":
		return level
	case "warn":
		return "warning"
	default:
		return "warning"
	}
}

func buildXrayInbound(in ExportInbound, sniffEnabled bool) map[string]any {
	clients := make([]map[string]any, 0, len(in.Clients))
	for _, c := range in.Clients {
		client := map[string]any{"email": c.Email}
		switch in.Protocol {
		case "vmess":
			client["id"] = c.UUID
		case "vless":
			client["id"] = c.UUID
			if c.Flow != "" {
				client["flow"] = c.Flow
			}
		case "trojan":
			client["password"] = c.Password
		case "shadowsocks":
			client["password"] = c.Password
			client["method"] = c.Method
		}
		clients = append(clients, client)
	}

	settings := map[string]any{"clients": clients}
	if in.Protocol == "vless" {
		settings["decryption"] = "none"
	}

	network := in.Network
	if network == "" {
		network = "tcp"
	}
	stream := map[string]any{"network": network}
	if network == "tcp" || network == "raw" {
		headerType := in.HeaderType
		if headerType == "" {
			headerType = "none"
		}
		stream["tcpSettings"] = map[string]any{"header": map[string]any{"type": headerType}}
	}

	switch in.Security {
	case "tls":
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]any{
			"serverName":   in.TLSServerName,
			"certificates": []map[string]any{{"certificateFile": "", "certificate": splitPEMLines(in.TLSCertificate), "keyFile": "", "key": splitPEMLines(in.TLSKey)}},
		}
	case "reality":
		stream["security"] = "reality"
		reality := map[string]any{
			"privateKey": in.RealityPrivateKey,
			"shortIds":   nonNilStrings(in.RealityShortIDs),
		}
		if in.RealityServerName != "" {
			reality["serverNames"] = []string{in.RealityServerName}
			if in.RealityServerPort != 0 {
				reality["dest"] = in.RealityServerName + ":" + itoa(int(in.RealityServerPort))
			}
		}
		stream["realitySettings"] = reality
	default:
		stream["security"] = "none"
	}

	// Xray's own multi-port syntax: a comma-separated string. One port keeps
	// the plain number every existing consumer already parses.
	var port any = in.Port
	if len(in.Ports) > 1 {
		port = joinInts(in.Ports)
	}

	inbound := map[string]any{
		"tag":            in.Tag,
		"port":           port,
		"protocol":       in.Protocol,
		"settings":       settings,
		"streamSettings": stream,
	}
	if sniffEnabled {
		inbound["sniffing"] = map[string]any{"enabled": true, "destOverride": []string{"http", "tls"}}
	}
	return inbound
}

// buildXrayOutbound is parseOutbound's inverse. Returns nil for a type
// with no real Xray equivalent (selector/urltest/hysteria2/tuic are
// sing-box-only concepts - parseOutbound itself can never produce one of
// these from a real Xray file, so a Core Config built entirely through
// import+dashboard editing never has them, but a config authored by hand
// in the dashboard could).
func buildXrayOutbound(ob Outbound) map[string]any {
	out := map[string]any{"tag": ob.Tag}
	stream := map[string]any{}
	if ob.BindInterface != "" {
		stream["sockopt"] = map[string]any{"interface": ob.BindInterface}
	}
	if ob.TLSEnabled {
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]any{"serverName": ob.TLSServerName, "allowInsecure": ob.TLSInsecure}
	}

	switch ob.Type {
	case "direct":
		out["protocol"] = "freedom"
	case "block":
		out["protocol"] = "blackhole"
	case "vmess", "vless":
		out["protocol"] = ob.Type
		user := map[string]any{"id": ob.UUID}
		if ob.Flow != "" {
			user["flow"] = ob.Flow
		}
		if ob.Security != "" {
			user["security"] = ob.Security
		}
		out["settings"] = map[string]any{"vnext": []map[string]any{{
			"address": ob.Server, "port": ob.ServerPort, "users": []map[string]any{user},
		}}}
	case "trojan", "shadowsocks", "socks", "http":
		out["protocol"] = ob.Type
		server := map[string]any{"address": ob.Server, "port": ob.ServerPort}
		if ob.Password != "" {
			server["password"] = ob.Password
		}
		if ob.Method != "" {
			server["method"] = ob.Method
		}
		if ob.Username != "" {
			server["user"] = ob.Username
		}
		out["settings"] = map[string]any{"servers": []map[string]any{server}}
	default:
		return nil
	}
	if len(stream) > 0 {
		out["streamSettings"] = stream
	}
	return out
}

// buildXrayRoutingRule is parseRoutingRule's inverse for the fields that
// round-trip through an Xray import (OutboundTag, Inbound - already real
// post-split tags, needing no further resolution to go back out).
// InboundPort goes out as Xray's localPort beside inboundTag (a
// comma-separated string, like the multi-port inbound "port"): it is how a
// multi-port inbound's per-port routing reads in Xray, though ParseXrayConfig
// turns it back into split tags rather than into InboundPort. The
// other routingRuleDTO fields (Domain/DomainSuffix/DomainKeyword/Port/
// PortRange/Protocol/IPIsPrivate) exist on RoutingRule only because it's
// shared with the dashboard's own Core Config editor - parseRoutingRule
// itself never populates them from a real Xray file - but a rule an
// admin authored by hand in that editor can have them, so they're still
// translated back into Xray's real field shapes here rather than
// silently dropped.
func buildXrayRoutingRule(r RoutingRule) map[string]any {
	rule := map[string]any{"type": "field", "outboundTag": r.OutboundTag}
	if len(r.Inbound) > 0 {
		rule["inboundTag"] = r.Inbound
	}
	if len(r.InboundPort) > 0 {
		rule["localPort"] = joinInts(r.InboundPort)
	}

	var domain []string
	domain = append(domain, r.DomainKeyword...)
	for _, d := range r.Domain {
		domain = append(domain, "full:"+d)
	}
	for _, d := range r.DomainSuffix {
		domain = append(domain, "domain:"+d)
	}
	if len(domain) > 0 {
		rule["domain"] = domain
	}

	var ip []string
	ip = append(ip, r.IPCIDR...)
	if r.IPIsPrivate {
		ip = append(ip, "geoip:private")
	}
	if len(ip) > 0 {
		rule["ip"] = ip
	}

	if len(r.Network) > 0 {
		rule["network"] = joinStrings(r.Network, ",")
	}
	if len(r.Protocol) > 0 {
		rule["protocol"] = r.Protocol
	}

	var ports []string
	for _, p := range r.Port {
		ports = append(ports, itoa(p))
	}
	ports = append(ports, r.PortRange...)
	if len(ports) > 0 {
		rule["port"] = joinStrings(ports, ",")
	}

	return rule
}

func splitPEMLines(pem string) []string {
	if pem == "" {
		return nil
	}
	var lines []string
	start := 0
	for i, r := range pem {
		if r == '\n' {
			lines = append(lines, pem[start:i])
			start = i + 1
		}
	}
	if start < len(pem) {
		lines = append(lines, pem[start:])
	}
	return lines
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func joinStrings(s []string, sep string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = itoa(n)
	}
	return joinStrings(parts, ",")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
