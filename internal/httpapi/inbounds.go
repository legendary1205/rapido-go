package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// handleListInbounds implements GET /api/inbounds, grouped by protocol like
// the current system.py's GET /api/inbounds (there, read live from the
// parsed xray/sing-box config; here, from the inbounds table until the
// node-agent phase syncs it from a live proxy core config for real - see
// 00002_inbound_protocol.sql).
func (h *Handler) handleListInbounds(c *gin.Context) {
	rows, err := h.store.Queries.ListInbounds(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list inbounds"})
		return
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.Protocol] = append(out[r.Protocol], r.Tag)
	}
	c.JSON(http.StatusOK, out)
}

type inboundSyncEntry struct {
	Tag      string `json:"tag" binding:"required"`
	Protocol string `json:"protocol" binding:"required"`
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
		row, err := h.store.Queries.UpsertInbound(c.Request.Context(), generated.UpsertInboundParams{Tag: e.Tag, Protocol: e.Protocol})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not sync inbound " + e.Tag})
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
// first time it's registered.
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

func proxyTypeValid(protocol string) bool {
	switch protocol {
	case "vmess", "vless", "trojan", "shadowsocks":
		return true
	}
	return false
}
