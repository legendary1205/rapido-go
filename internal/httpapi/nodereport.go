package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/hostmetrics"
)

const nodeIDContextKey = "node.id"

// requireNodeSecret authenticates POST /api/internal/node-report by bearer
// secret (nodes.report_secret), not an admin JWT - a node identifies
// itself this way instead of via mTLS since that direction (node -> panel)
// is the opposite of the mTLS the node's own control-plane already
// requires (panel -> node, see cmd/node/main.go). See migration 00006's
// doc comment for the full rationale.
func (h *Handler) requireNodeSecret(c *gin.Context) {
	const prefix = "Bearer "
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, prefix) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"detail": "Missing node report secret"})
		return
	}
	secret := strings.TrimPrefix(header, prefix)
	node, err := h.store.Queries.GetNodeByReportSecret(c.Request.Context(), pgtype.Text{String: secret, Valid: true})
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"detail": "Invalid node report secret"})
		return
	}
	c.Set(nodeIDContextKey, node.ID)
	c.Next()
}

type nodeReportUserUsage struct {
	Username string `json:"username" binding:"required"`
	Uplink   int64  `json:"uplink"`
	Downlink int64  `json:"downlink"`
}

type nodeReportRequest struct {
	Users []nodeReportUserUsage `json:"users"`
	Host  hostmetrics.Sample    `json:"host"`
}

// handleNodeReport implements POST /api/internal/node-report - the
// receiving half of the node agent's push loop (cmd/node/main.go). Must be
// pointed at by the backend-singleton instance specifically, not a
// load-balanced pool of stateless API replicas: h.hostMetricsTracker's
// rate/percent computation is only correct when exactly one process ever
// calls Derive for a given node - see PreviousTracker's own doc comment.
func (h *Handler) handleNodeReport(c *gin.Context) {
	nodeID := c.MustGet(nodeIDContextKey).(int32)

	var req nodeReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}

	ctx := c.Request.Context()

	if len(req.Users) > 0 {
		usernames := make([]string, len(req.Users))
		byUsername := make(map[string]nodeReportUserUsage, len(req.Users))
		for i, u := range req.Users {
			usernames[i] = u.Username
			byUsername[u.Username] = u
		}
		rows, err := h.store.Queries.GetUsersByUsernames(ctx, usernames)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not resolve users"})
			return
		}

		hourBucket := pgtype.Timestamptz{Time: time.Now().UTC().Truncate(time.Hour), Valid: true}
		var nodeUplink, nodeDownlink int64
		adminDeltas := make(map[int32]int64)

		for _, row := range rows {
			usage, ok := byUsername[row.Username]
			if !ok {
				continue
			}
			delta := usage.Uplink + usage.Downlink
			if delta <= 0 {
				continue
			}
			if err := h.store.Queries.IncrementUserUsage(ctx, generated.IncrementUserUsageParams{
				ID: row.ID, UsedTraffic: delta,
			}); err != nil {
				h.logger.Error("node report: increment user usage", "error", err, "user_id", row.ID)
				continue
			}
			if err := h.store.Queries.UpsertNodeUserUsage(ctx, generated.UpsertNodeUserUsageParams{
				CreatedAt: hourBucket, UserID: pgInt4FromInt(int(row.ID)), NodeID: pgInt4FromInt(int(nodeID)),
				UsedTraffic: pgInt8FromInt64(delta),
			}); err != nil {
				h.logger.Error("node report: upsert node_user_usage", "error", err)
			}
			if row.AdminID.Valid {
				adminDeltas[row.AdminID.Int32] += delta
			}
			nodeUplink += usage.Uplink
			nodeDownlink += usage.Downlink
		}

		for adminID, delta := range adminDeltas {
			if err := h.store.Queries.IncrementAdminUsage(ctx, generated.IncrementAdminUsageParams{
				ID: adminID, UsersUsage: delta,
			}); err != nil {
				h.logger.Error("node report: increment admin usage", "error", err, "admin_id", adminID)
			}
		}

		if nodeUplink > 0 || nodeDownlink > 0 {
			if err := h.store.Queries.UpsertNodeUsage(ctx, generated.UpsertNodeUsageParams{
				CreatedAt: hourBucket, NodeID: pgInt4FromInt(int(nodeID)),
				Uplink: pgInt8FromInt64(nodeUplink), Downlink: pgInt8FromInt64(nodeDownlink),
			}); err != nil {
				h.logger.Error("node report: upsert node_usage", "error", err)
			}
			if err := h.store.Queries.IncrementNodeCumulative(ctx, generated.IncrementNodeCumulativeParams{
				ID: nodeID, Uplink: pgInt8FromInt64(nodeUplink), Downlink: pgInt8FromInt64(nodeDownlink),
			}); err != nil {
				h.logger.Error("node report: increment node cumulative", "error", err)
			}
			if err := h.store.Queries.IncrementSystemCumulative(ctx, generated.IncrementSystemCumulativeParams{
				Uplink: pgInt8FromInt64(nodeUplink), Downlink: pgInt8FromInt64(nodeDownlink),
			}); err != nil {
				h.logger.Error("node report: increment system cumulative", "error", err)
			}
		}
	}

	if req.Host.CollectedAt.IsZero() {
		req.Host.CollectedAt = time.Now().UTC()
	}
	derived := h.hostMetricsTracker.Derive(nodeKey(nodeID), req.Host)
	if err := hostmetrics.Store(ctx, h.store.Queries, &nodeID, req.Host, derived, true); err != nil {
		h.logger.Error("node report: store host metric", "error", err)
	}

	if err := h.store.Queries.MarkNodeConnectedIfNotDisabled(ctx, nodeID); err != nil {
		h.logger.Error("node report: mark node connected", "error", err)
	}

	c.JSON(http.StatusOK, gin.H{"detail": "recorded"})
}

func nodeKey(nodeID int32) string {
	return "node:" + strconv.Itoa(int(nodeID))
}
