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
	"github.com/legendary1205/rapido-go/internal/kirbot"
)

// handleListInbounds implements GET /api/inbounds, grouped by protocol like
// the current system.py's GET /api/inbounds (there, read live from the
// parsed xray/sing-box config; here, from the inbounds table until the
// node-agent phase syncs it from a live proxy core config for real - see
// 00002_inbound_protocol.sql).
func (h *Handler) handleListInbounds(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.store.Queries.ListInbounds(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list inbounds"})
		return
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.Protocol] = append(out[r.Protocol], r.Tag)
	}

	// KirBot inbound filtering (app/kirbot/manager.py's get_configs, called
	// from app/routers/system.py's get_inbounds): a reseller only sees the
	// inbounds their external bot allows. Sudo always sees everything -
	// KirBot is never even called for a sudo admin.
	identity := auth.CurrentIdentity(c)
	if !identity.IsSudo {
		settings, _, err := h.resolveIntegrationSettings(c)
		if err == nil {
			if filtered := h.kirbot.GetConfigs(ctx, kirbot.Config{Secret: settings.KirbotSecret, URL: settings.KirbotURL}, identity.Username, out); len(filtered) > 0 {
				out = filtered
			}
		}
	}

	c.JSON(http.StatusOK, out)
}

// inboundDetailDTO is the full-fidelity shape a real "Inbounds" management
// page needs (tag/protocol/network/security/reality fields) - deliberately
// a NEW response shape on a NEW route rather than changing GET /api/inbounds
// itself, which stays the simple map[protocol][]tag shape the KirBot
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
// Cascades to that inbound's hosts/exclusions/template-associations at the
// DB level (see DeleteInboundByTag's own doc comment) - nothing here needs
// to clean those up itself, only the caches that would otherwise keep
// serving the deleted tag until their TTL.
func (h *Handler) handleDeleteInbound(c *gin.Context) {
	tag := c.Param("tag")
	ctx := c.Request.Context()

	inbound, err := h.store.Queries.GetInboundByTag(ctx, tag)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Inbound not found"})
		return
	}
	if err := h.store.Queries.DeleteInboundByTag(ctx, tag); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not delete inbound"})
		return
	}
	if err := h.store.InvalidateInbound(ctx, tag, inbound.Protocol, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not invalidate inbound cache"})
		return
	}
	if err := h.store.InvalidateHosts(ctx, tag); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not invalidate host cache"})
		return
	}
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Inbound removed successfully"})
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
// first time it's registered. No InvalidateHosts call needed: this only
// ever runs for a tag that handleSyncInbounds just inserted for the first
// time, so no CachedListHostsByInboundTag entry for it can exist yet.
func createDefaultHost(ctx context.Context, q *generated.Queries, tag string) error {
	_, err := q.CreateHost(ctx, generated.CreateHostParams{
		Remark:      "Rapido ({USERNAME}) [{PROTOCOL} - {TRANSPORT}]",
		Address:     "{SERVER_IP}",
		Security:    "inbound_default",
		Alpn:        "none",
		Fingerprint: "none",
		InboundTag:  tag,
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
