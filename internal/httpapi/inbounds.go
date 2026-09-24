package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/resellerapi"
)

// handleListInbounds implements GET /api/inbounds, grouped by protocol like
// the current system.py's GET /api/inbounds (there, read live from the
// parsed xray/sing-box config; here, from the inbounds table until the
// node-agent phase syncs it from a live proxy core config for real - see
// 00002_inbound_protocol.sql).
// proxyInboundDTO mirrors app/models/proxy.py's ProxyInbound exactly - all
// five fields, always present. The earlier shape here was a bare tag
// string per entry, which is a real incompatibility rather than a
// simplification: every Marzban-ecosystem client iterates this map and
// reads inbound["tag"] (plus port/network to build a config), and a string
// is not indexable, so such a client sees zero usable inbounds. One real
// reseller bot's symptom for that is refusing to sync "to avoid deleting
// tags by mistake" - it found no tags it could parse.
type proxyInboundDTO struct {
	Tag      string `json:"tag"`
	Protocol string `json:"protocol"`
	Network  string `json:"network"`
	TLS      string `json:"tls"`
	Port     int32  `json:"port"`
	// Ports is every distinct port of the inbound's enabled hosts, ascending
	// (always an array, empty when there are none). Port stays the primary
	// host's alone: the real Marzban model has no such field, so the one
	// every client reads must not change meaning.
	Ports []int `json:"ports"`
}

func (h *Handler) handleListInbounds(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.store.Queries.ListInboundsWithPort(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list inbounds"})
		return
	}

	byTag := make(map[string]proxyInboundDTO, len(rows))
	tagsByProtocol := map[string][]string{}
	for _, r := range rows {
		byTag[r.Tag] = proxyInboundDTO{
			Tag: r.Tag, Protocol: r.Protocol, Network: r.Network,
			// The real panel calls this field "tls" and puts the security
			// mode in it ("none"/"tls"/"reality"), which is what this
			// column already holds.
			TLS:   r.Security,
			Port:  r.Port.Int32,
			Ports: validHostPorts(r.Ports),
		}
		tagsByProtocol[r.Protocol] = append(tagsByProtocol[r.Protocol], r.Tag)
	}

	// the reseller API inbound filtering (app/resellerapi/manager.py's get_configs, called
	// from app/routers/system.py's get_inbounds): a reseller only sees the
	// inbounds their external bot allows. Sudo always sees everything -
	// the reseller API is never even called for a sudo admin. It filters tags, so it
	// runs on the tag map and the result is expanded back into objects.
	identity := auth.CurrentIdentity(c)
	if !identity.IsSudo {
		settings, _, err := h.resolveIntegrationSettings(c)
		if err == nil {
			if filtered := h.resellerapi.GetConfigs(ctx, resellerapi.Config{Secret: settings.ResellerApiSecret, URL: settings.ResellerApiUrl}, identity.Username, tagsByProtocol); len(filtered) > 0 {
				tagsByProtocol = filtered
			}
		}
	}

	out := make(map[string][]proxyInboundDTO, len(tagsByProtocol))
	for protocol, tags := range tagsByProtocol {
		entries := make([]proxyInboundDTO, 0, len(tags))
		for _, tag := range tags {
			if entry, ok := byTag[tag]; ok {
				entries = append(entries, entry)
				continue
			}
			// A tag the reseller API returned that this panel doesn't have: keep it
			// visible rather than dropping it silently, with the protocol
			// it was filed under.
			entries = append(entries, proxyInboundDTO{Tag: tag, Protocol: protocol, Network: "tcp", TLS: "none", Ports: []int{}})
		}
		out[protocol] = entries
	}

	c.JSON(http.StatusOK, out)
}

// inboundDetailDTO is the full-fidelity shape a real "Inbounds" management
// page needs (tag/protocol/network/security/reality fields) - deliberately
// a NEW response shape on a NEW route rather than changing GET /api/inbounds
// itself, which stays the simple map[protocol][]tag shape the the reseller API
// filtering logic and InboundsPicker.tsx already depend on.
type inboundDetailDTO struct {
	Tag        string `json:"tag"`
	Protocol   string `json:"protocol"`
	Network    string `json:"network"`
	HeaderType string `json:"header_type"`

	Security          string   `json:"security"`
	RealityPrivateKey string   `json:"reality_private_key,omitempty"`
	RealityShortIDs   []string `json:"reality_short_ids,omitempty"`
	RealityServerName string   `json:"reality_server_name,omitempty"`
	RealityServerPort int32    `json:"reality_server_port,omitempty"`

	// TLS* is only meaningful when Security is "tls" - a real customer-
	// facing certificate/key pair (plain PEM text, same no-encryption-at-
	// rest convention RealityPrivateKey already uses), NOT the panel's own
	// CA in the `tls` table used for node mTLS. An inbound with
	// Security="tls" and an empty TLSCertificate/TLSKey stays out of
	// automatic node sync until both are filled in - see
	// ListAutoSyncInbounds's own doc comment.
	TLSCertificate string `json:"tls_certificate,omitempty"`
	TLSKey         string `json:"tls_key,omitempty"`
	TLSServerName  string `json:"tls_server_name,omitempty"`
}

func toInboundDetailDTO(in generated.Inbound) inboundDetailDTO {
	// pgtype.Text/Int4's own zero value is "" / 0 when !Valid, exactly the
	// "unset" wire value this DTO wants - no helper needed.
	return inboundDetailDTO{
		Tag: in.Tag, Protocol: in.Protocol, Network: in.Network, HeaderType: in.HeaderType.String,
		Security:          in.Security,
		RealityPrivateKey: in.RealityPrivateKey.String,
		RealityShortIDs:   in.RealityShortIds,
		RealityServerName: in.RealityServerName.String,
		RealityServerPort: in.RealityServerPort.Int32,
		TLSCertificate:    in.TlsCertificate.String,
		TLSKey:            in.TlsKey.String,
		TLSServerName:     in.TlsServerName.String,
	}
}

// handleListInboundsDetailed implements GET /api/inbounds/detail (sudo
// only) - the real list a dedicated Inbounds admin page renders, as opposed
// to handleListInbounds's simpler protocol-grouped tag list every admin can
// read for the user-create form's inbound picker.
func (h *Handler) handleListInboundsDetailed(c *gin.Context) {
	rows, err := h.store.Queries.ListInbounds(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list inbounds"})
		return
	}
	out := make([]inboundDetailDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toInboundDetailDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

// handleDeleteInbound implements DELETE /api/inbounds/:tag (sudo only).
func (h *Handler) handleDeleteInbound(c *gin.Context) {
	tag := c.Param("tag")
	ctx := c.Request.Context()

	if err := h.deleteInboundByTag(ctx, tag); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"detail": "Inbound not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	proxiesRemoved, err := h.pruneOrphanedProxies(ctx)
	if err != nil {
		h.logger.Warn("prune orphaned proxies", "error", err)
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Inbound removed successfully", "orphaned_proxies_removed": proxiesRemoved})
}

// deleteInboundByTag is the real work behind DELETE /api/inbounds/:tag:
// remove the row (cascades to that inbound's hosts/exclusions/template-
// associations at the DB level, see DeleteInboundByTag's own doc comment)
// and invalidate the caches that would otherwise keep serving it until
// their TTL. Deliberately does NOT invalidate the node-config cache itself
// - callers that delete several tags in one pass (handleUpdateXrayConfig)
// only need to pay that one cost once, after the whole batch, not per tag.
func (h *Handler) deleteInboundByTag(ctx context.Context, tag string) error {
	inbound, err := h.store.Queries.GetInboundByTag(ctx, tag)
	if err != nil {
		return err
	}
	if err := h.store.Queries.DeleteInboundByTag(ctx, tag); err != nil {
		return fmt.Errorf("could not delete inbound %s: %w", tag, err)
	}
	if err := h.store.InvalidateInbound(ctx, tag, inbound.Protocol, nil); err != nil {
		return fmt.Errorf("could not invalidate inbound cache for %s: %w", tag, err)
	}
	return nil
}

type inboundSyncEntry struct {
	Tag        string `json:"tag" binding:"required"`
	Protocol   string `json:"protocol" binding:"required"`
	Network    string `json:"network"`     // tcp/ws/grpc/kcp/quic/splithttp/xhttp - defaults to "tcp"
	HeaderType string `json:"header_type"` // e.g. "http" for tcp obfuscation

	Security          string   `json:"security"` // none/tls/reality - defaults to "none"
	RealityPrivateKey string   `json:"reality_private_key,omitempty"`
	RealityShortIDs   []string `json:"reality_short_ids,omitempty"`
	RealityServerName string   `json:"reality_server_name,omitempty"`
	RealityServerPort int32    `json:"reality_server_port,omitempty"`

	TLSCertificate string `json:"tls_certificate,omitempty"`
	TLSKey         string `json:"tls_key,omitempty"`
	TLSServerName  string `json:"tls_server_name,omitempty"`
}

// syncInboundEntries is the real work behind POST /api/inbounds/sync:
// upsert-by-tag, creating a default host (no real port yet) for any
// genuinely new tag. Extracted out of handleSyncInbounds so the
// Xray-config importer (internal/httpapi/xrayimport.go) can reuse the
// exact same write path instead of duplicating it - unlike that HTTP
// handler, this returns a plain Go error (an unknown-protocol entry is
// reported the same way any other failure is, via the returned error, not
// a distinct HTTP status - both callers already validate protocol values
// upstream of this function in their own way).
func (h *Handler) syncInboundEntries(ctx context.Context, entries []inboundSyncEntry) (created int, err error) {
	for _, e := range entries {
		if !proxyTypeValid(e.Protocol) {
			return created, &inboundValidationError{"unknown protocol: " + e.Protocol}
		}
	}

	for _, e := range entries {
		network := e.Network
		if network == "" {
			network = "tcp"
		}
		security := e.Security
		if security == "" {
			security = "none"
		}

		var oldProtocol *string
		if existing, err := h.store.Queries.GetInboundByTag(ctx, e.Tag); err == nil {
			oldProtocol = &existing.Protocol
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return created, fmt.Errorf("could not sync inbound %s: %w", e.Tag, err)
		}

		row, err := h.store.Queries.UpsertInbound(ctx, generated.UpsertInboundParams{
			Tag: e.Tag, Protocol: e.Protocol, Network: network, HeaderType: textFromPtr(normalizeZeroString(&e.HeaderType)),
			Security:          security,
			RealityPrivateKey: textFromPtr(normalizeZeroString(&e.RealityPrivateKey)),
			RealityShortIds:   e.RealityShortIDs,
			RealityServerName: textFromPtr(normalizeZeroString(&e.RealityServerName)),
			RealityServerPort: realityPortToPg(e.RealityServerPort),
			TlsCertificate:    textFromPtr(normalizeZeroString(&e.TLSCertificate)),
			TlsKey:            textFromPtr(normalizeZeroString(&e.TLSKey)),
			TlsServerName:     textFromPtr(normalizeZeroString(&e.TLSServerName)),
		})
		if err != nil {
			return created, fmt.Errorf("could not sync inbound %s: %w", e.Tag, err)
		}
		if err := h.store.InvalidateInbound(ctx, e.Tag, e.Protocol, oldProtocol); err != nil {
			return created, fmt.Errorf("could not invalidate inbound cache for %s: %w", e.Tag, err)
		}
		if row.Inserted {
			if err := createDefaultHost(ctx, h.store.Queries, e.Tag); err != nil {
				return created, fmt.Errorf("could not create default host for %s: %w", e.Tag, err)
			}
			created++
		}
	}
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	return created, nil
}

// handleSyncInbounds implements POST /api/inbounds/sync (sudo only) - an
// interim stand-in for the real proxy-core-config sync the node-agent phase
// will do.
func (h *Handler) handleSyncInbounds(c *gin.Context) {
	var entries []inboundSyncEntry
	if err := c.ShouldBindJSON(&entries); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	created, err := h.syncInboundEntries(c.Request.Context(), entries)
	if err != nil {
		var verr *inboundValidationError
		if errors.As(err, &verr) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": verr.msg})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"synced": len(entries), "created": created})
}

// inboundValidationError marks an error from syncInboundEntries as a
// client mistake (400-shaped) rather than an internal failure - same
// pattern as coreconfig.go's coreConfigValidationError, for the same
// reason: this function's callers (the plain sync handler and the
// Xray-config importer) need to tell the two apart without this function
// itself knowing about gin/HTTP status codes.
type inboundValidationError struct{ msg string }

func (e *inboundValidationError) Error() string { return e.msg }

// createDefaultHost mirrors add_default_host in the current
// crud.get_or_create_inbound: every inbound gets one default ProxyHost the
// first time it's registered.
//
// Placed at the end of the existing global priority order (see migration
// 00008 and GetMaxHostPriority's own doc comment) rather than left at the
// priority column's bare default (0) - every host auto-created this way
// would otherwise land on the exact same priority, making the Hosts
// page's up/down reorder buttons a real no-op between any two of them
// (swapping two equal values changes nothing) without anything actually
// being broken in the swap logic itself.
func createDefaultHost(ctx context.Context, q *generated.Queries, tag string) error {
	maxPriority, err := q.GetMaxHostPriority(ctx)
	if err != nil {
		return fmt.Errorf("could not determine the next host priority: %w", err)
	}
	_, err = q.CreateHost(ctx, generated.CreateHostParams{
		Remark:      "Rapido ({USERNAME}) [{PROTOCOL} - {TRANSPORT}]",
		Address:     "{SERVER_IP}",
		Security:    "inbound_default",
		Alpn:        "none",
		Fingerprint: "none",
		InboundTag:  tag,
		Priority:    maxPriority + 1,
	})
	return err
}

func realityPortToPg(port int32) pgtype.Int4 {
	if port == 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: port, Valid: true}
}

func proxyTypeValid(protocol string) bool {
	switch protocol {
	case "vmess", "vless", "trojan", "shadowsocks":
		return true
	}
	return false
}

// pruneOrphanedProxies deletes every proxy whose protocol no longer has any
// inbound at all - see PruneOrphanedProxies's own doc comment for why this
// matters (a protocol dropped from the live config, e.g. going from
// vless+vmess+trojan down to vless-only, otherwise leaves dead vmess/trojan
// credentials sitting in every affected user's proxies forever).
//
// Deliberately refuses to run at all when there are currently zero inbounds
// of ANY protocol: PruneOrphanedProxies's `NOT IN (SELECT ... FROM
// inbounds)` would otherwise treat an empty inbounds table as "every
// protocol is orphaned" and delete every proxy for every user - a real risk
// during a legitimate "clear everything, then re-import" admin workflow
// (this migration's own Core Config reset did exactly that), not just a
// hypothetical edge case.
func (h *Handler) pruneOrphanedProxies(ctx context.Context) (int, error) {
	inbounds, err := h.store.Queries.ListInbounds(ctx)
	if err != nil {
		return 0, fmt.Errorf("could not list inbounds: %w", err)
	}
	if len(inbounds) == 0 {
		return 0, nil
	}
	removed, err := h.store.Queries.PruneOrphanedProxies(ctx)
	if err != nil {
		return 0, fmt.Errorf("could not prune orphaned proxies: %w", err)
	}
	return len(removed), nil
}
