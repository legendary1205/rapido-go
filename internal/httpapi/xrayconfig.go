package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// xrayConfigDTO merges coreConfigDTO and the inbounds list into one
// document - the user's own framing was that "Xray settings" (core config +
// inbounds) is conceptually one thing that this rewrite had split into two
// separate pages/tables, adding real complexity (two JSON editors, two
// Apply buttons, no single source of truth) for no benefit an admin
// actually wanted. GET/PUT /api/settings/xray-config is the merged
// replacement; the older GET/PUT /api/settings/core-config and
// POST /api/inbounds/sync endpoints are untouched underneath (still used
// directly by the Xray-config importer, xrayimport.go) - this is a new
// handler built on the exact same applyCoreConfig/syncInboundEntries
// helpers, not a parallel implementation.
type xrayConfigDTO struct {
	LogLevel     string             `json:"log_level"`
	SniffEnabled bool               `json:"sniff_enabled"`
	Outbounds    []outboundDTO      `json:"outbounds"`
	RoutingRules []routingRuleDTO   `json:"routing_rules"`
	DNSServers   []dnsServerDTO     `json:"dns_servers"`
	Inbounds     []inboundDetailDTO `json:"inbounds"`
	UpdatedAt    *time.Time         `json:"updated_at,omitempty"`
}

// handleGetXrayConfig implements GET /api/settings/xray-config (sudo only).
func (h *Handler) handleGetXrayConfig(c *gin.Context) {
	ctx := c.Request.Context()
	core, err := h.store.CachedGetCoreConfig(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read core config"})
		return
	}
	inboundRows, err := h.store.Queries.ListInbounds(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list inbounds"})
		return
	}
	coreDTO := toCoreConfigDTO(core)
	inbounds := make([]inboundDetailDTO, 0, len(inboundRows))
	for _, r := range inboundRows {
		inbounds = append(inbounds, toInboundDetailDTO(r))
	}
	c.JSON(http.StatusOK, xrayConfigDTO{
		LogLevel: coreDTO.LogLevel, SniffEnabled: coreDTO.SniffEnabled,
		Outbounds: coreDTO.Outbounds, RoutingRules: coreDTO.RoutingRules, DNSServers: coreDTO.DNSServers,
		Inbounds: inbounds, UpdatedAt: coreDTO.UpdatedAt,
	})
}

// handleUpdateXrayConfig implements PUT /api/settings/xray-config (sudo
// only) - a full-object replace of both halves together. Inbounds are
// synced FIRST, core config second: a routing rule's `inbound` list can
// legitimately reference a tag this same request is also introducing for
// the first time (add a new inbound and a rule that targets it in one
// edit), and applyCoreConfig's own routing-rule validation checks against
// whatever's already in the inbounds table at call time - if core config
// were applied first, that exact common case would be rejected as
// "targets unknown inbound" even though the request as a whole is
// perfectly valid. A cheap, DB-free validateCoreConfig pre-check still
// runs before touching anything, so a plain shape mistake (bad log_level,
// duplicate outbound tag, etc.) never has the side effect of creating
// inbound rows first. Not fully atomic beyond that pre-check (a DB error
// between the two writes leaves inbounds synced but core config not yet
// saved) - a deliberate, documented tradeoff over the complexity of a
// shared transaction, matching this project's "don't over-engineer" bar;
// the failure mode is "re-click Apply", never silent data loss, since
// inbound sync never deletes anything omitted from the payload either.
func (h *Handler) handleUpdateXrayConfig(c *gin.Context) {
	var dto xrayConfigDTO
	if err := c.ShouldBindJSON(&dto); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}

	coreDTO := coreConfigDTO{
		LogLevel: dto.LogLevel, SniffEnabled: dto.SniffEnabled,
		Outbounds: dto.Outbounds, RoutingRules: dto.RoutingRules, DNSServers: dto.DNSServers,
	}
	if coreDTO.LogLevel == "" {
		coreDTO.LogLevel = "warn"
	}
	if msg := validateCoreConfig(coreDTO); msg != "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": msg})
		return
	}

	ctx := c.Request.Context()

	// Full replace, same convention PUT /hosts already established: a tag
	// missing from this request gets deleted, not silently left behind -
	// the one real behavior difference from POST /api/inbounds/sync (an
	// upsert-only endpoint that never deletes, kept exactly as-is for its
	// own direct callers, see syncInboundEntries's own doc comment). This
	// is deliberate here, not incidental: once inbounds only had this one
	// JSON editor to go through (the old per-row Delete button on the
	// retired InboundsAdmin.tsx is gone), upsert-only would have made
	// removing an inbound impossible from the dashboard entirely. Snapshot
	// BEFORE syncing so a tag this same request is also renaming-by-tag
	// (delete old, create new) isn't confused with one genuinely removed.
	previousTags, err := h.store.Queries.ListInboundTags(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list inbounds"})
		return
	}

	entries := make([]inboundSyncEntry, 0, len(dto.Inbounds))
	submittedTags := make(map[string]bool, len(dto.Inbounds))
	for _, in := range dto.Inbounds {
		entries = append(entries, inboundSyncEntry{
			Tag: in.Tag, Protocol: in.Protocol, Network: in.Network, HeaderType: in.HeaderType,
			Security: in.Security, RealityPrivateKey: in.RealityPrivateKey, RealityShortIDs: in.RealityShortIDs,
			RealityServerName: in.RealityServerName, RealityServerPort: in.RealityServerPort,
			TLSCertificate: in.TLSCertificate, TLSKey: in.TLSKey, TLSServerName: in.TLSServerName,
		})
		submittedTags[in.Tag] = true
	}
	if _, err := h.syncInboundEntries(ctx, entries); err != nil {
		var verr *inboundValidationError
		if errors.As(err, &verr) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": verr.msg})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}

	for _, tag := range previousTags {
		if submittedTags[tag] {
			continue
		}
		if err := h.deleteInboundByTag(ctx, tag); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
			return
		}
	}

	savedCore, err := h.applyCoreConfig(ctx, coreDTO)
	if err != nil {
		var verr *coreConfigValidationError
		if errors.As(err, &verr) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": verr.msg})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}

	inboundRows, err := h.store.Queries.ListInbounds(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list inbounds"})
		return
	}
	inbounds := make([]inboundDetailDTO, 0, len(inboundRows))
	for _, r := range inboundRows {
		inbounds = append(inbounds, toInboundDetailDTO(r))
	}
	c.JSON(http.StatusOK, xrayConfigDTO{
		LogLevel: savedCore.LogLevel, SniffEnabled: savedCore.SniffEnabled,
		Outbounds: savedCore.Outbounds, RoutingRules: savedCore.RoutingRules, DNSServers: savedCore.DNSServers,
		Inbounds: inbounds, UpdatedAt: savedCore.UpdatedAt,
	})
}
