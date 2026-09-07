package xrayimport

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// validHostALPN/validHostFingerprint mirror internal/httpapi/hosts.go's own
// validAlpn/validFingerprint sets exactly (that file's own CHECK-constraint-
// backed enums) - duplicated here rather than imported, since importing
// internal/httpapi from this package would be a circular import (httpapi
// is the one that will call into this package). A value outside these
// sets would fail the hosts table's real CHECK constraint at insert time,
// so this package must defend against that itself and degrade to "none"
// with a warning rather than let a downstream INSERT fail opaquely.
var validHostALPN = map[string]bool{
	"none": true, "h3": true, "h2": true, "http/1.1": true,
	"h3,h2,http/1.1": true, "h3,h2": true, "h2,http/1.1": true,
}
var validHostFingerprint = map[string]bool{
	"none": true, "chrome": true, "firefox": true, "safari": true, "ios": true,
	"android": true, "edge": true, "360": true, "qq": true, "random": true, "randomized": true,
}
var validProtocols = map[string]bool{"vmess": true, "vless": true, "trojan": true, "shadowsocks": true}

// splitTag is one (port, new tag) pair a single Xray inbound with several
// comma-separated ports expands into - see ParseXrayConfig's own doc
// comment on why this split is unavoidable (sing-box, like this codebase's
// own schema, is one port per inbound tag).
type splitTag struct {
	port int
	tag  string
}

// ParseXrayConfig is the whole package's entry point - a pure function,
// no I/O, no DB. See Result's own doc comment for what it returns.
func ParseXrayConfig(raw []byte) (Result, error) {
	var cfg xrayConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Result{}, fmt.Errorf("not valid JSON: %w", err)
	}
	if len(cfg.Inbounds) == 0 && len(cfg.Outbounds) == 0 {
		return Result{}, fmt.Errorf("no inbounds or outbounds found - is this a real Xray config file?")
	}

	result := Result{
		LogLevel: mapLogLevel(cfg.Log.LogLevel),
	}

	tagSplits := make(map[string][]splitTag, len(cfg.Inbounds))
	for _, in := range cfg.Inbounds {
		entries, splits, warnings := parseInbound(in)
		result.Inbounds = append(result.Inbounds, entries...)
		result.Warnings = append(result.Warnings, warnings...)
		tagSplits[in.Tag] = splits
		if in.Sniffing.Enabled {
			result.SniffEnabled = true
		}
	}

	reservedOutboundTags := map[string]bool{"direct": true, "block": true}
	for _, ob := range cfg.Outbounds {
		parsed, warning := parseOutbound(ob)
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
		if parsed == nil {
			continue
		}
		if reservedOutboundTags[parsed.Tag] {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"outbound %q: this codebase already reserves the tags \"direct\"/\"block\" for the two always-present built-in outbounds - skipped, rename it in the source config and re-import if you need it", parsed.Tag))
			continue
		}
		result.Outbounds = append(result.Outbounds, *parsed)
	}

	if cfg.Routing.DomainStrategy != "" {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"routing.domainStrategy %q has no equivalent here and was dropped", cfg.Routing.DomainStrategy))
	}
	for i, r := range cfg.Routing.Rules {
		rule, warning := parseRoutingRule(r, tagSplits)
		if warning != "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("routing rule #%d: %s", i+1, warning))
		}
		if rule != nil {
			result.RoutingRules = append(result.RoutingRules, *rule)
		}
	}

	result.DNSServers, result.Warnings = parseDNSServers(cfg.DNS.Servers, result.Warnings)

	return result, nil
}

// mapLogLevel translates Xray's log levels (debug/info/warning/error/none)
// to sing-box's (trace/debug/info/warn/error/fatal/panic) - "warn" is a
// safe default for anything unrecognized, including Xray's "none" (which
// sing-box has no direct equivalent for).
func mapLogLevel(level string) string {
	switch level {
	case "debug", "info", "error":
		return level
	case "warning":
		return "warn"
	default:
		return "warn"
	}
}

// parsePortList handles both of Xray's port shapes for the same field - a
// bare JSON number, or a comma-separated string ("20000,20004,20008"). A
// dash-range segment ("1000-2000") is not expanded (it could mean
// thousands of inbound tags, one per port, in this codebase's one-port-
// per-tag model) - it's reported as a warning and skipped instead.
func parsePortList(raw json.RawMessage, warnOwner string) ([]int, []string) {
	if len(raw) == 0 {
		return nil, nil
	}
	var asNumber float64
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return []int{int(asNumber)}, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return nil, []string{fmt.Sprintf("%s: port field is neither a number nor a string, skipped", warnOwner)}
	}
	var ports []int
	var warnings []string
	for _, part := range strings.Split(asString, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			warnings = append(warnings, fmt.Sprintf(
				"%s: port range %q is not supported (would need one inbound tag per port) - skipped, add specific ports instead", warnOwner, part))
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: could not parse port %q, skipped", warnOwner, part))
			continue
		}
		ports = append(ports, n)
	}
	return ports, warnings
}

// parseInbound expands one Xray inbound (possibly several ports) into one
// Inbound entry per port, plus the splitTag list parseRoutingRule needs to
// resolve inboundTag+localPort references back to the right new tag(s).
func parseInbound(in xrayInbound) ([]Inbound, []splitTag, []string) {
	var warnings []string
	if !validProtocols[in.Protocol] {
		return nil, nil, []string{fmt.Sprintf("inbound %q: protocol %q is not supported here (only vmess/vless/trojan/shadowsocks), skipped entirely", in.Tag, in.Protocol)}
	}
	ports, portWarnings := parsePortList(in.Port, fmt.Sprintf("inbound %q", in.Tag))
	warnings = append(warnings, portWarnings...)
	if len(ports) == 0 {
		warnings = append(warnings, fmt.Sprintf("inbound %q: no usable port found, skipped entirely", in.Tag))
		return nil, nil, warnings
	}

	if len(in.Settings.Fallbacks) > 0 {
		warnings = append(warnings, fmt.Sprintf("inbound %q: %d fallback(s) have no sing-box equivalent and were dropped", in.Tag, len(in.Settings.Fallbacks)))
	}

	headerType := ""
	if in.StreamSettings.TCPSettings != nil {
		headerType = in.StreamSettings.TCPSettings.Header.Type
	}

	var tlsCert, tlsKey, tlsServerName, hostALPN, hostFingerprint string
	if tls := in.StreamSettings.TLSSettings; tls != nil {
		tlsServerName = tls.ServerName
		if tls.MinVersion != "" {
			warnings = append(warnings, fmt.Sprintf("inbound %q: tlsSettings.minVersion has no equivalent here and was dropped", in.Tag))
		}
		if len(tls.Certificates) > 1 {
			warnings = append(warnings, fmt.Sprintf("inbound %q: %d certificates found, only the first is supported here - the rest were dropped", in.Tag, len(tls.Certificates)))
		}
		if len(tls.Certificates) > 0 {
			cert := tls.Certificates[0]
			tlsCert = strings.Join(cert.Certificate, "\n")
			tlsKey = strings.Join(cert.Key, "\n")
		}
		if len(tls.Alpn) > 0 {
			joined := strings.Join(tls.Alpn, ",")
			if validHostALPN[joined] {
				hostALPN = joined
			} else {
				warnings = append(warnings, fmt.Sprintf("inbound %q: alpn %q isn't one of the combinations this panel supports, dropped from the default host", in.Tag, joined))
			}
		}
		if tls.Fingerprint != "" {
			if validHostFingerprint[tls.Fingerprint] {
				hostFingerprint = tls.Fingerprint
			} else {
				warnings = append(warnings, fmt.Sprintf("inbound %q: fingerprint %q isn't recognized, dropped from the default host", in.Tag, tls.Fingerprint))
			}
		}
	}

	var realityPrivateKey, realityServerName string
	var realityShortIDs []string
	var realityServerPort int32
	if rl := in.StreamSettings.RealitySettings; rl != nil {
		realityPrivateKey = rl.PrivateKey
		realityShortIDs = rl.ShortIds
		if len(rl.ServerNames) > 0 {
			realityServerName = rl.ServerNames[0]
		}
		if rl.Dest != "" {
			if _, portStr, ok := strings.Cut(rl.Dest, ":"); ok {
				if p, err := strconv.Atoi(portStr); err == nil {
					realityServerPort = int32(p)
				}
			}
		}
	}

	multiPort := len(ports) > 1
	entries := make([]Inbound, 0, len(ports))
	splits := make([]splitTag, 0, len(ports))
	for _, port := range ports {
		tag := in.Tag
		if multiPort {
			tag = fmt.Sprintf("%s-%d", in.Tag, port)
		}
		entries = append(entries, Inbound{
			Tag: tag, Protocol: in.Protocol, Network: in.StreamSettings.Network, HeaderType: headerType,
			Security:          in.StreamSettings.Security,
			RealityPrivateKey: realityPrivateKey, RealityShortIDs: realityShortIDs,
			RealityServerName: realityServerName, RealityServerPort: realityServerPort,
			TLSCertificate: tlsCert, TLSKey: tlsKey, TLSServerName: tlsServerName,
			HostALPN: hostALPN, HostFingerprint: hostFingerprint, Port: int32(port),
		})
		splits = append(splits, splitTag{port: port, tag: tag})
	}
	return entries, splits, warnings
}

// parseOutbound returns (nil, "") for a silently-skipped implicit-shaped
// case that needs no warning, (nil, warning) for a skip that does, or a
// real Outbound. protocol/tag are always required by Xray itself, so
// those aren't re-validated here.
func parseOutbound(ob xrayOutbound) (*Outbound, string) {
	out := &Outbound{Tag: ob.Tag}
	if ob.StreamSettings.Sockopt != nil {
		out.BindInterface = ob.StreamSettings.Sockopt.Interface
	}

	switch ob.Protocol {
	case "freedom":
		out.Type = "direct"
		return out, ""
	case "blackhole":
		out.Type = "block"
		return out, ""
	case "vmess", "vless":
		out.Type = ob.Protocol
		var s xrayOutboundVnextSettings
		if err := json.Unmarshal(ob.Settings, &s); err != nil || len(s.Vnext) == 0 || len(s.Vnext[0].Users) == 0 {
			return nil, fmt.Sprintf("outbound %q: could not find a usable vnext/users entry for a %s outbound, skipped", ob.Tag, ob.Protocol)
		}
		out.Server = s.Vnext[0].Address
		out.ServerPort = s.Vnext[0].Port
		out.UUID = s.Vnext[0].Users[0].ID
		out.Flow = s.Vnext[0].Users[0].Flow
		out.Security = s.Vnext[0].Users[0].Security
		applyOutboundTLS(out, ob.StreamSettings)
		return out, ""
	case "trojan", "shadowsocks", "socks", "http":
		out.Type = ob.Protocol
		var s xrayOutboundServerSettings
		if err := json.Unmarshal(ob.Settings, &s); err != nil || len(s.Servers) == 0 {
			return nil, fmt.Sprintf("outbound %q: could not find a usable servers entry for a %s outbound, skipped", ob.Tag, ob.Protocol)
		}
		out.Server = s.Servers[0].Address
		out.ServerPort = s.Servers[0].Port
		out.Password = s.Servers[0].Password
		out.Method = s.Servers[0].Method
		out.Username = s.Servers[0].User
		applyOutboundTLS(out, ob.StreamSettings)
		return out, ""
	case "":
		return nil, ""
	default:
		return nil, fmt.Sprintf("outbound %q: protocol %q is not supported here, skipped", ob.Tag, ob.Protocol)
	}
}

func applyOutboundTLS(out *Outbound, stream xrayStreamSettings) {
	if stream.Security != "tls" || stream.TLSSettings == nil {
		return
	}
	out.TLSEnabled = true
	out.TLSServerName = stream.TLSSettings.ServerName
}

// parseRoutingRule resolves one Xray routing rule into this codebase's
// inbound-tag-based shape. A rule naming both an inboundTag and a
// localPort matches exactly the one split tag for that port; one naming
// only an inboundTag (Xray's own catch-all pattern, see the plan's real
// sample - the trailing unconditional "route to blackhole" rule per node)
// matches every split tag that original inbound produced.
func parseRoutingRule(r xrayRoutingRule, tagSplits map[string][]splitTag) (*RoutingRule, string) {
	if r.Type != "" && r.Type != "field" {
		return nil, fmt.Sprintf("rule type %q is not supported here, skipped", r.Type)
	}
	rule := &RoutingRule{OutboundTag: r.OutboundTag}

	if len(r.InboundTag) == 0 {
		return rule, ""
	}
	ports, portWarnings := parsePortList(r.LocalPort, "routing rule localPort")
	var warning string
	if len(portWarnings) > 0 {
		warning = strings.Join(portWarnings, "; ")
	}

	for _, origTag := range r.InboundTag {
		splits, ok := tagSplits[origTag]
		if !ok {
			warning = appendWarning(warning, fmt.Sprintf("references inbound tag %q, which wasn't found among this file's own inbounds, skipped for that tag", origTag))
			continue
		}
		if len(ports) == 0 {
			// No localPort named - matches every port that original tag
			// expanded into.
			for _, s := range splits {
				rule.Inbound = append(rule.Inbound, s.tag)
			}
			continue
		}
		for _, p := range ports {
			found := false
			for _, s := range splits {
				if s.port == p {
					rule.Inbound = append(rule.Inbound, s.tag)
					found = true
					break
				}
			}
			if !found {
				warning = appendWarning(warning, fmt.Sprintf("localPort %d doesn't match any port inbound %q actually has, skipped", p, origTag))
			}
		}
	}
	return rule, warning
}

func appendWarning(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}

// parseDNSServers handles only Xray's plain-string DNS server shorthand
// (a bare IP address, exactly what the plan's real sample uses) -
// structured DNS server objects (with their own address/port/domains
// fields) are a real Xray feature but not one this importer's primary
// target needs, so an entry that isn't a plain string is reported and
// skipped rather than guessed at.
func parseDNSServers(raw []json.RawMessage, warnings []string) ([]DNSServer, []string) {
	var servers []DNSServer
	for i, entry := range raw {
		var addr string
		if err := json.Unmarshal(entry, &addr); err != nil {
			warnings = append(warnings, fmt.Sprintf("dns.servers[%d]: structured DNS server objects aren't supported here, only plain address strings - skipped", i))
			continue
		}
		servers = append(servers, DNSServer{Tag: fmt.Sprintf("dns-%d", i+1), Type: "udp", Address: addr})
	}
	return servers, warnings
}
