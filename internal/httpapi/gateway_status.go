package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// computePanelCrowdedness is Gateway sub-phase 3: a live load signal for
// THIS panel, sub-phase 4's subscription merge will use to rank this
// panel's hosts against a peer's. Deliberately just one number - the sum
// of every currently-online node's TCP ESTABLISHED count
// (host_metrics.connections, already collected by every node's regular
// report, see internal/hostmetrics) - not a composite with CPU/bandwidth.
// A real, already-meaningful signal beats a made-up weighted formula
// this project has no data to tune yet; CPU/bandwidth can be folded in
// later if connections alone proves too coarse in practice.
//
// Reuses GetLatestHostMetricPerNode (monitoring.go's own query - one row
// per node, already deduped by MAX(id)) and monitoring.go's own
// `staleAfter` cutoff (2 minutes, 4x the collector's report interval): a
// node that stopped reporting is excluded entirely rather than counted
// with its last-known (now meaningless) connection count, which would
// otherwise make a dead node look like it's still carrying live traffic
// and never get load balanced around. The panel's own node_id-NULL
// self-sample row is skipped too - it isn't a node serving client
// traffic at all.
func (h *Handler) computePanelCrowdedness(ctx context.Context) (int, error) {
	rows, err := h.store.Queries.GetLatestHostMetricPerNode(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	now := time.Now()
	for _, m := range rows {
		if !m.NodeID.Valid {
			continue
		}
		if !m.CollectedAt.Valid || now.Sub(m.CollectedAt.Time) > staleAfter {
			continue
		}
		if m.Connections.Valid {
			total += int(m.Connections.Int32)
		}
	}
	return total, nil
}

// gatewayHostDTO is hostDTO plus what a peer actually needs to use this
// host that GET /api/hosts's own tag-grouped response doesn't carry: which
// inbound tag and protocol it belongs to, flattened into each entry since
// this response isn't grouped the way the admin-facing one is.
type gatewayHostDTO struct {
	hostDTO
	InboundTag string `json:"inbound_tag"`
	Protocol   string `json:"protocol"`
}

type gatewayStatusDTO struct {
	Crowdedness int              `json:"crowdedness"`
	Hosts       []gatewayHostDTO `json:"hosts"`
}

// handleGatewayStatus implements GET /api/internal/gateway/status
// (panel-to-panel, requireGatewaySecret) - what sub-phase 4's background
// refresh job will poll on every enabled peer. Hosts are filtered to
// non-disabled only, same as ListHostsByInboundTag already does for
// subscription serving - a peer has no more use for a disabled host than
// this panel's own subscription generation does.
func (h *Handler) handleGatewayStatus(c *gin.Context) {
	ctx := c.Request.Context()

	crowdedness, err := h.computePanelCrowdedness(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not compute crowdedness: " + err.Error()})
		return
	}

	hostRows, err := h.store.Queries.ListHosts(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not list hosts: " + err.Error()})
		return
	}
	inboundRows, err := h.store.Queries.ListInbounds(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "could not list inbounds: " + err.Error()})
		return
	}
	protocolByTag := make(map[string]string, len(inboundRows))
	for _, in := range inboundRows {
		protocolByTag[in.Tag] = in.Protocol
	}

	hosts := make([]gatewayHostDTO, 0, len(hostRows))
	for _, r := range hostRows {
		if r.IsDisabled.Valid && r.IsDisabled.Bool {
			continue
		}
		hosts = append(hosts, gatewayHostDTO{
			hostDTO: toHostDTO(r), InboundTag: r.InboundTag, Protocol: protocolByTag[r.InboundTag],
		})
	}

	c.JSON(http.StatusOK, gatewayStatusDTO{Crowdedness: crowdedness, Hosts: hosts})
}
