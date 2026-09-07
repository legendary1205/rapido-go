package httpapi

import (
	"encoding/json"
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

var validOutboundTypes = map[string]bool{"direct": true, "block": true, "socks": true, "http": true, "selector": true, "urltest": true}
var validDNSTypes = map[string]bool{"local": true, "udp": true, "tcp": true, "tls": true, "https": true}
var validLogLevels = map[string]bool{"trace": true, "debug": true, "info": true, "warn": true, "error": true, "fatal": true, "panic": true}
var validNetworks = map[string]bool{"tcp": true, "udp": true, "icmp": true}
var validProtocols = map[string]bool{"tls": true, "http": true, "quic": true, "dns": true, "stun": true, "bittorrent": true, "dtls": true, "ssh": true, "rdp": true, "ntp": true}

type outboundDTO struct {
	Tag        string   `json:"tag" binding:"required"`
	Type       string   `json:"type" binding:"required"`
	Server     string   `json:"server,omitempty"`
	ServerPort int      `json:"server_port,omitempty"`
	Username   string   `json:"username,omitempty"`
	Password   string   `json:"password,omitempty"`
	Outbounds  []string `json:"outbounds,omitempty"` // selector/urltest member tags
}

type routingRuleDTO struct {
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
		if (ob.Type == "socks" || ob.Type == "http") && (ob.Server == "" || ob.ServerPort == 0) {
			return "outbound " + ob.Tag + ": server and server_port are required for type " + ob.Type
		}
		if (ob.Type == "selector" || ob.Type == "urltest") && len(ob.Outbounds) == 0 {
			return "outbound " + ob.Tag + ": at least one member outbound is required for type " + ob.Type
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

// handleUpdateCoreConfig implements PUT /api/settings/core-config (sudo
// only) - a full-object replace, matching PUT /hosts's own convention.
func (h *Handler) handleUpdateCoreConfig(c *gin.Context) {
	var dto coreConfigDTO
	if err := c.ShouldBindJSON(&dto); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	if dto.LogLevel == "" {
		dto.LogLevel = "warn"
	}
	if msg := validateCoreConfig(dto); msg != "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": msg})
		return
	}

	outboundsJSON, _ := json.Marshal(dto.Outbounds)
	rulesJSON, _ := json.Marshal(dto.RoutingRules)
	dnsJSON, _ := json.Marshal(dto.DNSServers)

	ctx := c.Request.Context()
	row, err := h.store.Queries.UpdateCoreConfig(ctx, generated.UpdateCoreConfigParams{
		LogLevel: dto.LogLevel, SniffEnabled: dto.SniffEnabled,
		Outbounds: outboundsJSON, RoutingRules: rulesJSON, DnsServers: dnsJSON,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not save core config"})
		return
	}
	if err := h.store.InvalidateCoreConfig(ctx); err != nil {
		h.logger.Warn("invalidate core config cache", "error", err)
	}
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	c.JSON(http.StatusOK, toCoreConfigDTO(row))
}
