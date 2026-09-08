package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/subscription"
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

// gatewayStatusHostDTO is exactly subscription.EffectiveInbound (the same
// merged host+inbound view forEachUserHost already builds for every LOCAL
// host, via subscription.BuildEffectiveInbound) plus the two things that
// view alone doesn't carry: the host's own raw remark template (never
// pre-formatted here - remarkVars.Format uses the REQUESTING panel's own
// user variables, not this panel's, so the template has to cross the wire
// as-is) and its Priority (peer hosts are still ranked among themselves
// by that, layered under the receiving panel's own live crowdedness
// ranking - see forEachUserHost's peer-merge step).
type gatewayStatusHostDTO struct {
	subscription.EffectiveInbound
	Remark   string `json:"remark"`
	Priority int32  `json:"priority"`
}

type gatewayStatusDTO struct {
	Crowdedness int                    `json:"crowdedness"`
	Hosts       []gatewayStatusHostDTO `json:"hosts"`
}

// handleGatewayStatus implements GET /api/internal/gateway/status
// (panel-to-panel, requireGatewaySecret) - what the gatewayjob background
// refresh loop polls on every enabled peer (sub-phase 4). Hosts are
// filtered to non-disabled only, same as ListHostsByInboundTag already
// does for this panel's own subscription serving. Deliberately sends the
// already-merged EffectiveInbound, not raw host+inbound rows: it's the
// exact same public-safe view (real Reality PUBLIC key, never the private
// key it's derived from) forEachUserHost already builds for local hosts,
// so no new "what's safe to expose" analysis was needed - it's the same
// answer subscription generation already gave.
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

	hosts := make([]gatewayStatusHostDTO, 0, len(hostRows))
	for _, r := range hostRows {
		if r.IsDisabled.Valid && r.IsDisabled.Bool {
			continue
		}
		inbound, err := h.store.CachedGetInboundByTag(ctx, r.InboundTag)
		if err != nil {
			continue
		}
		eff := subscription.BuildEffectiveInbound(inbound, r)
		hosts = append(hosts, gatewayStatusHostDTO{EffectiveInbound: eff, Remark: r.Remark, Priority: r.Priority})
	}

	c.JSON(http.StatusOK, gatewayStatusDTO{Crowdedness: crowdedness, Hosts: hosts})
}
