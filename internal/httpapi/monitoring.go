package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// staleAfter mirrors app/routers/monitoring.py's own 2-minute cutoff (4x
// the collector's 30s interval).
const staleAfter = 2 * time.Minute

type monitoringHostDTO struct {
	NodeID       *int32     `json:"node_id"`
	Name         string     `json:"name"`
	Address      string     `json:"address,omitempty"`
	Reachable    bool       `json:"reachable"`
	CollectedAt  *time.Time `json:"collected_at"`
	Stale        bool       `json:"stale"`
	CPUPercent   *float64   `json:"cpu_percent"`
	MemPercent   *float64   `json:"mem_percent"`
	DiskPercent  *float64   `json:"disk_percent"`
	RxRate       *int64     `json:"rx_rate"`
	TxRate       *int64     `json:"tx_rate"`
	Connections  *int32     `json:"connections"`
	TunnelsUp    *int32     `json:"tunnels_up"`
	TunnelsTotal *int32     `json:"tunnels_total"`
	Healthy      bool       `json:"healthy"`
}

func toMonitoringHostDTO(name, address string, nodeID *int32, m *generated.HostMetric) monitoringHostDTO {
	dto := monitoringHostDTO{NodeID: nodeID, Name: name, Address: address}
	if m == nil {
		return dto
	}
	dto.Reachable = true
	collectedAt := m.CollectedAt.Time
	dto.CollectedAt = &collectedAt
	dto.Stale = time.Since(collectedAt) > staleAfter
	dto.CPUPercent = pgFloat8ToPtr(m.CpuPercent)
	dto.MemPercent = pgFloat8ToPtr(m.MemPercent)
	dto.DiskPercent = pgFloat8ToPtr(m.DiskPercent)
	dto.RxRate = int8ToPtr(m.RxRate)
	dto.TxRate = int8ToPtr(m.TxRate)
	dto.Connections = pgInt4ToPtr(m.Connections)
	dto.TunnelsUp = pgInt4ToPtr(m.TunnelsUp)
	dto.TunnelsTotal = pgInt4ToPtr(m.TunnelsTotal)
	dto.Healthy = m.Healthy
	return dto
}

func pgFloat8ToPtr(v pgtype.Float8) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

// handleGetMonitoring implements GET /api/monitoring (sudo only) - one
// host-health snapshot per node plus the panel's own self-sample, sourced
// entirely from what's already been pushed/self-sampled (no live poll of
// anything happens here, unlike the current Python system's own pull
// model - see the Phase 7.3 plan's context for why).
func (h *Handler) handleGetMonitoring(c *gin.Context) {
	ctx := c.Request.Context()
	nodes, err := h.store.Queries.ListNodes(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list nodes"})
		return
	}
	latest, err := h.store.Queries.GetLatestHostMetricPerNode(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read monitoring data"})
		return
	}
	byNodeID := make(map[int32]*generated.HostMetric)
	var panelMetric *generated.HostMetric
	for i := range latest {
		m := &latest[i]
		if m.NodeID.Valid {
			byNodeID[m.NodeID.Int32] = m
		} else {
			panelMetric = m
		}
	}

	hosts := make([]monitoringHostDTO, 0, len(nodes)+1)
	hosts = append(hosts, toMonitoringHostDTO("Panel", "", nil, panelMetric))
	for _, n := range nodes {
		id := n.ID
		hosts = append(hosts, toMonitoringHostDTO(n.Name, n.Address, &id, byNodeID[n.ID]))
	}

	c.JSON(http.StatusOK, gin.H{"hosts": hosts, "generated_at": time.Now().UTC()})
}

type historyPointDTO struct {
	CollectedAt time.Time `json:"t"`
	CPUPercent  *float64  `json:"cpu"`
	MemPercent  *float64  `json:"mem"`
	RxRate      *int64    `json:"rx"`
	TxRate      *int64    `json:"tx"`
	Connections *int32    `json:"conns"`
}

// handleGetMonitoringHistory implements
// GET /api/monitoring/history?node_id=&hours= (sudo only, node_id omitted
// means the panel's own history). hours clamps to [1,48], matching Python.
func (h *Handler) handleGetMonitoringHistory(c *gin.Context) {
	hours := 6
	if v := c.Query("hours"); v != "" {
		if n, err := parseClampedInt(v, 1, 48); err == nil {
			hours = n
		}
	}
	var nodeID pgtype.Int4
	if v := c.Query("node_id"); v != "" {
		if n, err := parseClampedInt(v, 0, 1<<30); err == nil {
			nodeID = pgtype.Int4{Int32: int32(n), Valid: true}
		}
	}

	rows, err := h.store.Queries.GetHostMetricHistory(c.Request.Context(), generated.GetHostMetricHistoryParams{
		CollectedAt: timestamptzFromTime(time.Now().UTC().Add(-time.Duration(hours) * time.Hour)),
		NodeID:      nodeID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read monitoring history"})
		return
	}
	out := make([]historyPointDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, historyPointDTO{
			CollectedAt: r.CollectedAt.Time,
			CPUPercent:  pgFloat8ToPtr(r.CpuPercent),
			MemPercent:  pgFloat8ToPtr(r.MemPercent),
			RxRate:      int8ToPtr(r.RxRate),
			TxRate:      int8ToPtr(r.TxRate),
			Connections: pgInt4ToPtr(r.Connections),
		})
	}
	c.JSON(http.StatusOK, out)
}

func parseClampedInt(v string, min, max int) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, err
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n, nil
}
