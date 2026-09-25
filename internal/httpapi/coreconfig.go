package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// implicitOutboundTags are always available as a routing-rule target even
// with zero custom outbounds configured - cmd/node/main.go's buildOptions
// always emits both, matching the same "direct-out"/"block-out" tags a
// node without any Core Config at all has used since Phase 3.
var implicitOutboundTags = map[string]bool{"direct": true, "block": true}

// validOutboundTypes covers sing-box's real outbound registry (see
// internal/nodecore/registry.go's OutboundRegistry) minus WireGuard -
// WireGuard is a sing-box "Endpoint", a structurally different top-level
// config section (option.Options.Endpoints, not Outbounds, with its own
// registry this codebase doesn't wire in at all yet) rather than another
// outbound type, so it needs its own future phase, not a field bolted
// onto this one.
var validOutboundTypes = map[string]bool{
	"direct": true, "block": true, "socks": true, "http": true,
	"shadowsocks": true, "vmess": true, "trojan": true, "vless": true,
	"hysteria2": true, "tuic": true, "selector": true, "urltest": true,
}
var validDNSTypes = map[string]bool{"local": true, "udp": true, "tcp": true, "tls": true, "https": true}
var validLogLevels = map[string]bool{"trace": true, "debug": true, "info": true, "warn": true, "error": true, "fatal": true, "panic": true}
var validNetworks = map[string]bool{"tcp": true, "udp": true, "icmp": true}
var validProtocols = map[string]bool{"tls": true, "http": true, "quic": true, "dns": true, "stun": true, "bittorrent": true, "dtls": true, "ssh": true, "rdp": true, "ntp": true}
var validShadowsocksMethods = map[string]bool{
	"none": true, "aes-128-gcm": true, "aes-192-gcm": true, "aes-256-gcm": true,
	"chacha20-ietf-poly1305": true, "xchacha20-ietf-poly1305": true,
	"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true, "2022-blake3-chacha20-poly1305": true,
}
var validVMessSecurity = map[string]bool{"auto": true, "none": true, "zero": true, "aes-128-gcm": true, "chacha20-poly1305": true}
var validCongestionControl = map[string]bool{"cubic": true, "new_reno": true, "bbr": true}

type outboundDTO struct {
	Tag        string   `json:"tag" binding:"required"`
	Type       string   `json:"type" binding:"required"`
	Server     string   `json:"server,omitempty"`
	ServerPort int      `json:"server_port,omitempty"`
	Username   string   `json:"username,omitempty"`  // socks/http
	Password   string   `json:"password,omitempty"`  // socks/http/shadowsocks/trojan/hysteria2/tuic
	Outbounds  []string `json:"outbounds,omitempty"` // selector/urltest member tags

	UUID              string `json:"uuid,omitempty"`               // vmess/vless/tuic
	Flow              string `json:"flow,omitempty"`               // vless (optional - e.g. "xtls-rprx-vision")
	Method            string `json:"method,omitempty"`             // shadowsocks
	Security          string `json:"security,omitempty"`           // vmess encryption
	CongestionControl string `json:"congestion_control,omitempty"` // tuic

	// TLS* is the common subset every TLS-capable outbound type here
	// shares (vmess/trojan/vless/hysteria2/tuic) - a deliberately small
	// slice of sing-box's full OutboundTLSOptions (no ALPN/cert-pinning/
	// client-cert fields), matching this form's "commonly-needed fields
	// only" scope everywhere else.
	TLSEnabled    bool   `json:"tls_enabled,omitempty"`
	TLSServerName string `json:"tls_server_name,omitempty"`
	TLSInsecure   bool   `json:"tls_insecure,omitempty"`

	// BindInterface binds this outbound's dialer to a specific network
	// interface (e.g. a WireGuard tunnel device name) - the sing-box
	// equivalent of Xray's streamSettings.sockopt.interface, which the
	// real production fleet's per-location exit selection depends on
	// entirely. Meaningless for selector/urltest (group types have no
	// dialer of their own); harmless if set on one, just never read.
	BindInterface string `json:"bind_interface,omitempty"`

	// DirectFallback makes a bind_interface outbound fail open: while that
	// interface is missing or unhealthy on a node, connections dial without
	// the bind (a plain direct route) instead of failing. Only meaningful
	// with BindInterface, and only on a leaf type.
	DirectFallback bool `json:"direct_fallback,omitempty"`
}

type routingRuleDTO struct {
	// Inbound matches by which inbound tag a connection arrived through -
	// the mechanism that lets one rule route only e.g. "node1"'s traffic
	// to a specific outbound, matching Xray's inboundTag routing-rule
	// field. Unlike Domain/IPCIDR/etc (free-form data an admin types),
	// entries here must be real inbound tags - validated against the
	// live inbounds table in handleUpdateCoreConfig, not here (this DTO
	// has no DB access).
	Inbound []string `json:"inbound,omitempty"`
	// InboundPort narrows Inbound to the LOCAL listen ports the connection
	// arrived on - what lets one multi-port inbound route each port to its
	// own exit. Not checked against the inbound's hosts: a port that has no
	// host yet simply never matches.
	InboundPort   []int    `json:"inbound_port,omitempty"`
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`
	IPIsPrivate   bool     `json:"ip_is_private,omitempty"`
	Port          []int    `json:"port,omitempty"`
	PortRange     []string `json:"port_range,omitempty"`
	Network       []string `json:"network,omitempty"`
	Protocol      []string `json:"protocol,omitempty"`
	OutboundTag   string   `json:"outbound_tag" binding:"required"`
}

type dnsServerDTO struct {
	Tag     string `json:"tag" binding:"required"`
	Type    string `json:"type" binding:"required"`
	Address string `json:"address,omitempty"`
	Port    int    `json:"port,omitempty"`
	Path    string `json:"path,omitempty"`
}

type coreConfigDTO struct {
	LogLevel     string           `json:"log_level"`
	SniffEnabled bool             `json:"sniff_enabled"`
	Outbounds    []outboundDTO    `json:"outbounds"`
	RoutingRules []routingRuleDTO `json:"routing_rules"`
	DNSServers   []dnsServerDTO   `json:"dns_servers"`
	UpdatedAt    *time.Time       `json:"updated_at,omitempty"`
}

func toCoreConfigDTO(row generated.CoreConfig) coreConfigDTO {
	dto := coreConfigDTO{
		LogLevel: row.LogLevel, SniffEnabled: row.SniffEnabled,
		Outbounds: []outboundDTO{}, RoutingRules: []routingRuleDTO{}, DNSServers: []dnsServerDTO{},
	}
	_ = json.Unmarshal(row.Outbounds, &dto.Outbounds)
	_ = json.Unmarshal(row.RoutingRules, &dto.RoutingRules)
	_ = json.Unmarshal(row.DnsServers, &dto.DNSServers)
	if row.UpdatedAt.Valid {
		t := row.UpdatedAt.Time
		dto.UpdatedAt = &t
	}
	return dto
}

// handleGetCoreConfig implements GET /api/settings/core-config (sudo only).
func (h *Handler) handleGetCoreConfig(c *gin.Context) {
	row, err := h.store.CachedGetCoreConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read core config"})
		return
	}
	c.JSON(http.StatusOK, toCoreConfigDTO(row))
}

// validateCoreConfig mirrors the current Python system's server-side
// structural validation (XRayConfig's own tag-uniqueness/non-empty checks)
// adapted to this DTO shape - full-object replace, so everything is
// re-checked on every PUT, same as PUT /hosts's own convention.
func validateCoreConfig(dto coreConfigDTO) string {
	if dto.LogLevel != "" && !validLogLevels[dto.LogLevel] {
		return "invalid log_level"
	}
	tags := map[string]bool{}
	for _, ob := range dto.Outbounds {
		if ob.Tag == "" || implicitOutboundTags[ob.Tag] {
			return "outbound tag must be non-empty and not 'direct'/'block' (reserved)"
		}
		if tags[ob.Tag] {
			return "duplicate outbound tag: " + ob.Tag
		}
		tags[ob.Tag] = true
		if !validOutboundTypes[ob.Type] {
			return "invalid outbound type: " + ob.Type
		}
		needsServer := ob.Type == "socks" || ob.Type == "http" || ob.Type == "shadowsocks" ||
			ob.Type == "vmess" || ob.Type == "trojan" || ob.Type == "vless" || ob.Type == "hysteria2" || ob.Type == "tuic"
		if needsServer && (ob.Server == "" || ob.ServerPort == 0) {
			return "outbound " + ob.Tag + ": server and server_port are required for type " + ob.Type
		}
		if (ob.Type == "selector" || ob.Type == "urltest") && len(ob.Outbounds) == 0 {
			return "outbound " + ob.Tag + ": at least one member outbound is required for type " + ob.Type
		}
		if ob.DirectFallback {
			if ob.BindInterface == "" {
				return "outbound " + ob.Tag + ": direct_fallback requires bind_interface"
			}
			if ob.Type == "selector" || ob.Type == "urltest" || ob.Type == "block" {
				return "outbound " + ob.Tag + ": direct_fallback is not allowed for type " + ob.Type
			}
		}
		switch ob.Type {
		case "shadowsocks":
			if !validShadowsocksMethods[ob.Method] {
				return "outbound " + ob.Tag + ": invalid shadowsocks method " + ob.Method
			}
			if ob.Password == "" {
				return "outbound " + ob.Tag + ": password is required for type shadowsocks"
			}
		case "vmess":
			if ob.UUID == "" {
				return "outbound " + ob.Tag + ": uuid is required for type vmess"
			}
			if !validVMessSecurity[ob.Security] {
				return "outbound " + ob.Tag + ": invalid vmess security " + ob.Security
			}
		case "trojan":
			if ob.Password == "" {
				return "outbound " + ob.Tag + ": password is required for type trojan"
			}
		case "vless":
			if ob.UUID == "" {
				return "outbound " + ob.Tag + ": uuid is required for type vless"
			}
		case "hysteria2":
			if ob.Password == "" {
				return "outbound " + ob.Tag + ": password is required for type hysteria2"
			}
		case "tuic":
			if ob.UUID == "" || ob.Password == "" {
				return "outbound " + ob.Tag + ": uuid and password are required for type tuic"
			}
			if ob.CongestionControl != "" && !validCongestionControl[ob.CongestionControl] {
				return "outbound " + ob.Tag + ": invalid congestion_control " + ob.CongestionControl
			}
		}
	}
	// Member references (selector/urltest) and routing-rule targets both
	// resolve against the same tag universe: custom tags just defined,
	// plus the two implicit built-ins every node always has.
	validTarget := func(tag string) bool { return implicitOutboundTags[tag] || tags[tag] }
	for _, ob := range dto.Outbounds {
		for _, member := range ob.Outbounds {
			if !validTarget(member) {
				return "outbound " + ob.Tag + ": unknown member outbound " + member
			}
		}
	}
	for _, rule := range dto.RoutingRules {
		if !validTarget(rule.OutboundTag) {
			return "routing rule targets unknown outbound: " + rule.OutboundTag
		}
		for _, n := range rule.Network {
			if !validNetworks[n] {
				return "routing rule: invalid network " + n
			}
		}
		for _, p := range rule.Protocol {
			if !validProtocols[p] {
				return "routing rule: invalid protocol " + p
			}
		}
		if len(rule.InboundPort) > 0 {
			if len(rule.Inbound) == 0 {
				return "routing rule: inbound_port requires a non-empty inbound"
			}
			seen := make(map[int]bool, len(rule.InboundPort))
			for _, p := range rule.InboundPort {
				if p < 1 || p > 65535 {
					return fmt.Sprintf("routing rule: invalid inbound_port %d (must be 1-65535)", p)
				}
				if seen[p] {
					return fmt.Sprintf("routing rule: duplicate inbound_port %d", p)
				}
				seen[p] = true
			}
		}
	}
	dnsTags := map[string]bool{}
	for _, srv := range dto.DNSServers {
		if srv.Tag == "" {
			return "dns server tag is required"
		}
		if dnsTags[srv.Tag] {
			return "duplicate dns server tag: " + srv.Tag
		}
		dnsTags[srv.Tag] = true
		if !validDNSTypes[srv.Type] {
			return "invalid dns server type: " + srv.Type
		}
		if srv.Type != "local" && srv.Address == "" {
			return "dns server " + srv.Tag + ": address is required for type " + srv.Type
		}
	}
	return ""
}

// coreConfigValidationError marks an error as a 400-shaped admin mistake
// (bad field value, unknown reference) rather than an internal failure -
// applyCoreConfig's callers (the plain PUT handler and the Xray-config
// importer) both need to tell the two apart to pick the right HTTP status/
// response shape, without applyCoreConfig itself knowing about gin.
type coreConfigValidationError struct{ msg string }

func (e *coreConfigValidationError) Error() string { return e.msg }

// applyCoreConfig is the real work behind PUT /api/settings/core-config:
// validate, persist, invalidate the caches a stale read would otherwise
// serve from. Extracted out of handleUpdateCoreConfig so the Xray-config
// importer (internal/httpapi/xrayimport.go) can reuse the exact same
// validation and write path instead of duplicating it.
func (h *Handler) applyCoreConfig(ctx context.Context, dto coreConfigDTO) (coreConfigDTO, error) {
	if dto.LogLevel == "" {
		dto.LogLevel = "warn"
	}
	if msg := validateCoreConfig(dto); msg != "" {
		return coreConfigDTO{}, &coreConfigValidationError{msg}
	}

	// A routing rule's `inbound` entries reference a separate resource
	// (the inbounds table) that isn't part of this request body at all,
	// unlike outbound_tag (validated inside validateCoreConfig against
	// this same DTO's own outbound list) - so this check needs a real
	// query and lives here instead of in that pure function.
	needsInboundTags := false
	for _, rule := range dto.RoutingRules {
		if len(rule.Inbound) > 0 {
			needsInboundTags = true
			break
		}
	}
	if needsInboundTags {
		tags, err := h.store.Queries.ListInboundTags(ctx)
		if err != nil {
			return coreConfigDTO{}, fmt.Errorf("could not validate routing rules: %w", err)
		}
		known := make(map[string]bool, len(tags))
		for _, t := range tags {
			known[t] = true
		}
		for _, rule := range dto.RoutingRules {
			for _, tag := range rule.Inbound {
				if !known[tag] {
					return coreConfigDTO{}, &coreConfigValidationError{"routing rule targets unknown inbound: " + tag}
				}
			}
		}
	}

	if msg, err := h.validateNodeOverridesAgainstFleet(ctx, dto); err != nil {
		return coreConfigDTO{}, fmt.Errorf("could not validate node overrides: %w", err)
	} else if msg != "" {
		return coreConfigDTO{}, &coreConfigValidationError{msg}
	}

	outboundsJSON, _ := json.Marshal(dto.Outbounds)
	rulesJSON, _ := json.Marshal(dto.RoutingRules)
	dnsJSON, _ := json.Marshal(dto.DNSServers)

	row, err := h.store.Queries.UpdateCoreConfig(ctx, generated.UpdateCoreConfigParams{
		LogLevel: dto.LogLevel, SniffEnabled: dto.SniffEnabled,
		Outbounds: outboundsJSON, RoutingRules: rulesJSON, DnsServers: dnsJSON,
	})
	if err != nil {
		return coreConfigDTO{}, fmt.Errorf("could not save core config: %w", err)
	}
	if err := h.store.InvalidateCoreConfig(ctx); err != nil {
		h.logger.Warn("invalidate core config cache", "error", err)
	}
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	return toCoreConfigDTO(row), nil
}

// handleUpdateCoreConfig implements PUT /api/settings/core-config (sudo
// only) - a full-object replace, matching PUT /hosts's own convention.
func (h *Handler) handleUpdateCoreConfig(c *gin.Context) {
	var dto coreConfigDTO
	if err := c.ShouldBindJSON(&dto); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	saved, err := h.applyCoreConfig(c.Request.Context(), dto)
	if err != nil {
		var verr *coreConfigValidationError
		if errors.As(err, &verr) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": verr.msg})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, saved)
}
