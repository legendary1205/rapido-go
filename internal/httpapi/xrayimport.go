package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/xrayimport"
)

type xrayImportRequest struct {
	Config string `json:"config" binding:"required"`
	// Confirm defaults to false (Go's own zero value) - a request that
	// omits it entirely gets the safe preview-only behavior, matching the
	// same "explicit confirm before anything real happens" convention
	// Phase 8.1's restore endpoints and Phase 8.2's legacy import already
	// established.
	Confirm bool `json:"confirm"`
}

type xrayImportResultDTO struct {
	Applied           bool     `json:"applied"`
	InboundsCreated   int      `json:"inbounds_created"`
	OutboundsSaved    int      `json:"outbounds_saved"`
	RoutingRulesSaved int      `json:"routing_rules_saved"`
	DNSServersSaved   int      `json:"dns_servers_saved"`
	Warnings          []string `json:"warnings"`
}

// handleImportXrayConfig implements POST /api/inbounds/import-xray (sudo
// only) - parses a raw Xray core-config JSON file (internal/xrayimport)
// and, once confirmed, writes every inbound/host/outbound/routing-rule/
// dns-server it can map onto this codebase's own native shapes, reusing
// the exact same write paths POST /api/inbounds/sync and PUT
// /api/settings/core-config already use (syncInboundEntries/
// applyCoreConfig) rather than duplicating them.
//
// Deliberately additive, never a full replace: existing hosts an admin
// has already customized are left alone unless their inbound tag is one
// this import touches (in which case the host is replaced with a fresh
// default carrying the real imported port - see the loop below), and the
// fleet-wide Core Config is merged into (new outbounds/routing-rules/dns
// servers appended, not a wholesale replacement) rather than overwritten,
// so importing a second file doesn't wipe a first import's or an admin's
// own manual Core Config work.
func (h *Handler) handleImportXrayConfig(c *gin.Context) {
	var req xrayImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}

	parsed, err := xrayimport.ParseXrayConfig([]byte(req.Config))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "could not parse this as an Xray config: " + err.Error()})
		return
	}

	result := xrayImportResultDTO{
		Applied:           req.Confirm,
		InboundsCreated:   len(parsed.Inbounds),
		OutboundsSaved:    len(parsed.Outbounds),
		RoutingRulesSaved: len(parsed.RoutingRules),
		DNSServersSaved:   len(parsed.DNSServers),
		Warnings:          parsed.Warnings,
	}
	if !req.Confirm {
		c.JSON(http.StatusOK, result)
		return
	}

	if badRequest, err := h.applyParsedXrayConfig(c.Request.Context(), parsed); err != nil {
		status := http.StatusInternalServerError
		if badRequest {
			status = http.StatusUnprocessableEntity
		}
		c.JSON(status, gin.H{"detail": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

// applyParsedXrayConfig writes an already-parsed Xray config through this
// codebase's own write paths - inbound sync, one real-port host per
// inbound, then the merged Core Config. Shared by POST
// /api/inbounds/import-xray and PUT /api/core/config (the route external
// reseller bots use), so the two can never drift into applying the
// same file differently. badRequest is true when the caller's payload is
// what's wrong, false when this panel failed to write it.
func (h *Handler) applyParsedXrayConfig(ctx context.Context, parsed xrayimport.Result) (badRequest bool, err error) {
	entries := make([]inboundSyncEntry, 0, len(parsed.Inbounds))
	for _, in := range parsed.Inbounds {
		entries = append(entries, inboundSyncEntry{
			Tag: in.Tag, Protocol: in.Protocol, Network: in.Network, HeaderType: in.HeaderType,
			Security:          in.Security,
			RealityPrivateKey: in.RealityPrivateKey, RealityShortIDs: in.RealityShortIDs,
			RealityServerName: in.RealityServerName, RealityServerPort: in.RealityServerPort,
			TLSCertificate: in.TLSCertificate, TLSKey: in.TLSKey, TLSServerName: in.TLSServerName,
		})
	}
	if _, err := h.syncInboundEntries(ctx, entries); err != nil {
		return false, fmt.Errorf("could not save the imported inbounds: %w", err)
	}

	// syncInboundEntries's own default host has no port at all (see
	// createDefaultHost's doc comment) - every imported inbound needs its
	// host replaced with one carrying the real port from the source
	// config, or it can never pass ListAutoSyncInbounds's own port-not-
	// null requirement. Also carries forward the alpn/fingerprint this
	// package pulled off the inbound's own tlsSettings, since those live
	// on the host in this codebase's model.
	//
	// nextPriority is computed once, up front, rather than re-queried on
	// every iteration: this loop deletes each tag's existing host right
	// before recreating it, so a fresh MAX(priority) query mid-loop could
	// read back a lower value than it should the moment the current global
	// max happens to be one of the hosts just deleted. Incrementing a
	// local counter avoids that, and still gives each newly-created host
	// its own distinct value instead of colliding on the column default
	// (see createDefaultHost's own doc comment on exactly that bug).
	maxPriority, err := h.store.Queries.GetMaxHostPriority(ctx)
	if err != nil {
		return false, fmt.Errorf("could not determine the next host priority: %w", err)
	}
	nextPriority := maxPriority + 1
	for _, in := range parsed.Inbounds {
		if err := h.store.Queries.DeleteHostsByInboundTag(ctx, in.Tag); err != nil {
			return false, fmt.Errorf("could not set up the host for %s: %w", in.Tag, err)
		}
		alpn, fingerprint := in.HostALPN, in.HostFingerprint
		if alpn == "" {
			alpn = "none"
		}
		if fingerprint == "" {
			fingerprint = "none"
		}
		if _, err := h.store.Queries.CreateHost(ctx, generated.CreateHostParams{
			Remark: "Rapido ({USERNAME}) [{PROTOCOL} - {TRANSPORT}]", Address: "{SERVER_IP}",
			Port: pgInt4FromInt(int(in.Port)), Security: "inbound_default", Alpn: alpn, Fingerprint: fingerprint,
			InboundTag: in.Tag, Priority: nextPriority,
		}); err != nil {
			return false, fmt.Errorf("could not set up the host for %s: %w", in.Tag, err)
		}
		nextPriority++
	}

	merged, err := h.mergeCoreConfigForImport(ctx, parsed)
	if err != nil {
		return false, fmt.Errorf("could not read the current core config: %w", err)
	}
	if _, err := h.applyCoreConfig(ctx, merged); err != nil {
		return true, fmt.Errorf("imported inbounds/hosts saved, but the core config could not be merged in: %w", err)
	}
	return false, nil
}

// mergeCoreConfigForImport folds a parsed Xray file's outbounds/routing
// rules/dns servers into whatever Core Config already exists, rather than
// replacing it outright - a second import (or an admin's own prior manual
// Core Config work) must never be silently wiped by a later one. Outbounds
// and DNS servers are keyed by tag (an import re-run with the same source
// file is idempotent, not a grower); routing rules have no natural
// identity to dedupe on, so imported ones are simply appended after
// whatever's already there.
func (h *Handler) mergeCoreConfigForImport(ctx context.Context, parsed xrayimport.Result) (coreConfigDTO, error) {
	existingRow, err := h.store.CachedGetCoreConfig(ctx)
	if err != nil {
		return coreConfigDTO{}, fmt.Errorf("could not read current core config: %w", err)
	}
	merged := toCoreConfigDTO(existingRow)

	existingOutboundTags := make(map[string]bool, len(merged.Outbounds))
	for _, ob := range merged.Outbounds {
		existingOutboundTags[ob.Tag] = true
	}
	for _, ob := range parsed.Outbounds {
		if existingOutboundTags[ob.Tag] {
			continue
		}
		merged.Outbounds = append(merged.Outbounds, outboundDTO{
			Tag: ob.Tag, Type: ob.Type, Server: ob.Server, ServerPort: ob.ServerPort,
			Username: ob.Username, Password: ob.Password, Outbounds: ob.Outbounds,
			UUID: ob.UUID, Flow: ob.Flow, Method: ob.Method, Security: ob.Security,
			CongestionControl: ob.CongestionControl,
			TLSEnabled:        ob.TLSEnabled, TLSServerName: ob.TLSServerName, TLSInsecure: ob.TLSInsecure,
			BindInterface: ob.BindInterface,
		})
		existingOutboundTags[ob.Tag] = true
	}

	for _, r := range parsed.RoutingRules {
		merged.RoutingRules = append(merged.RoutingRules, routingRuleDTO{
			Inbound: r.Inbound, Domain: r.Domain, DomainSuffix: r.DomainSuffix, DomainKeyword: r.DomainKeyword,
			IPCIDR: r.IPCIDR, IPIsPrivate: r.IPIsPrivate, Port: r.Port, PortRange: r.PortRange,
			Network: r.Network, Protocol: r.Protocol, OutboundTag: r.OutboundTag,
		})
	}

	existingDNSTags := make(map[string]bool, len(merged.DNSServers))
	for _, s := range merged.DNSServers {
		existingDNSTags[s.Tag] = true
	}
	for _, s := range parsed.DNSServers {
		if existingDNSTags[s.Tag] {
			continue
		}
		merged.DNSServers = append(merged.DNSServers, dnsServerDTO{Tag: s.Tag, Type: s.Type, Address: s.Address, Port: s.Port, Path: s.Path})
		existingDNSTags[s.Tag] = true
	}

	if parsed.LogLevel != "" {
		merged.LogLevel = parsed.LogLevel
	}
	merged.SniffEnabled = merged.SniffEnabled || parsed.SniffEnabled

	return merged, nil
}
