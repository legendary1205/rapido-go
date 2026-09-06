package httpapi

import (
	"context"
	"errors"
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
}

// handleSyncInbounds implements POST /api/inbounds/sync (sudo only) - an
// interim stand-in for the real proxy-core-config sync the node-agent phase
// will do. Newly-registered tags get a default host, mirroring
// add_default_host in the current crud.get_or_create_inbound.
func (h *Handler) handleSyncInbounds(c *gin.Context) {
	var entries []inboundSyncEntry
	if err := c.ShouldBindJSON(&entries); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	for _, e := range entries {
		if !proxyTypeValid(e.Protocol) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "unknown protocol: " + e.Protocol})
			return
		}
	}

	created := 0
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
		if existing, err := h.store.Queries.GetInboundByTag(c.Request.Context(), e.Tag); err == nil {
			oldProtocol = &existing.Protocol
		} else if !errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not sync inbound " + e.Tag})
			return
		}

		row, err := h.store.Queries.UpsertInbound(c.Request.Context(), generated.UpsertInboundParams{
			Tag: e.Tag, Protocol: e.Protocol, Network: network, HeaderType: textFromPtr(normalizeZeroString(&e.HeaderType)),
			Security:          security,
			RealityPrivateKey: textFromPtr(normalizeZeroString(&e.RealityPrivateKey)),
			RealityShortIds:   e.RealityShortIDs,
			RealityServerName: textFromPtr(normalizeZeroString(&e.RealityServerName)),
			RealityServerPort: realityPortToPg(e.RealityServerPort),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not sync inbound " + e.Tag})
			return
		}
		if err := h.store.InvalidateInbound(c.Request.Context(), e.Tag, e.Protocol, oldProtocol); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not invalidate inbound cache for " + e.Tag})
			return
		}
		if row.Inserted {
			if err := createDefaultHost(c.Request.Context(), h.store.Queries, e.Tag); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create default host for " + e.Tag})
				return
			}
			created++
		}
	}
	c.JSON(http.StatusOK, gin.H{"synced": len(entries), "created": created})
}

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
