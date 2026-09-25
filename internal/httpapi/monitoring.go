package httpapi

import (
	"encoding/json"
	"math"
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

// monitoringTunnelDTO is one WireGuard tunnel as the real panel reports
// it - a list of objects, not just the two counts this used to expose, so
// a client can name the tunnel that is down instead of only knowing that
// one of N is.
type monitoringTunnelDTO struct {
	Name string `json:"name"`
	Up   bool   `json:"up"`
	// Present is false when the tunnel is configured on the host but its
	// interface does not exist right now - a different failure from an
	// interface that is up with a stale handshake.
	Present bool  `json:"present"`
	RxBytes int64 `json:"rx_bytes"`
	TxBytes int64 `json:"tx_bytes"`
	// LastHandshake is unix seconds of the most recent peer handshake, 0 when
	// unknown or never. HandshakeAgeSeconds is the same fact as of the
	// sample's collection time (minimum across peers), null when no peer has
	// ever handshaked.
	LastHandshake       int64    `json:"last_handshake"`
	HandshakeAgeSeconds *float64 `json:"handshake_age_seconds"`
	ProbeMS             *float64 `json:"probe_ms"`
	Error               string   `json:"error,omitempty"`
	FallbackActive      bool     `json:"fallback_active"`
}

type monitoringHostDTO struct {
	NodeID      *int32     `json:"node_id"`
	Name        string     `json:"name"`
	Address     *string    `json:"address"`
	Reachable   bool       `json:"reachable"`
	CollectedAt *time.Time `json:"collected_at"`
	Stale       bool       `json:"stale"`
	CPUPercent  *float64   `json:"cpu_percent"`
	MemPercent  *float64   `json:"mem_percent"`
	DiskPercent *float64   `json:"disk_percent"`
	RxRate      *int64     `json:"rx_rate"`
	TxRate      *int64     `json:"tx_rate"`
	Connections *int32     `json:"connections"`
	// ClientConns is the node's open client connections (its presence total) at
	// the time of the sample: what a node's capacity is measured against, and
	// unlike Connections it leaves out the node's own upstream sockets. Null for
	// the panel and for a node that does not report it.
	ClientConns *int32 `json:"client_conns"`

	Uptime      *float64 `json:"uptime"`
	Load1m      *float64 `json:"load_1m"`
	XrayRunning *bool    `json:"xray_running"`
	XrayVersion *string  `json:"xray_version"`

	Tunnels    []monitoringTunnelDTO `json:"tunnels"`
	HasMetrics bool                  `json:"has_metrics"`

	// Kept alongside the fields above: this panel's own dashboard reads
	// them, and extra keys break no one.
	TunnelsUp    *int32 `json:"tunnels_up"`
	TunnelsTotal *int32 `json:"tunnels_total"`
	Healthy      bool   `json:"healthy"`
}

// hostMetricPayload is the subset of a stored sample the monitoring
// response exposes beyond the summarized columns.
type hostMetricPayload struct {
	CollectedAt   time.Time `json:"collected_at"`
	UptimeSeconds float64   `json:"uptime_seconds"`
	Load1m        float64   `json:"load_1m"`
	XrayRunning   bool      `json:"xray_running"`
	XrayVersion   string    `json:"xray_version"`
	Tunnels       []struct {
		Name string `json:"name"`
		Up   bool   `json:"up"`
		// Absent in samples stored before the node reported it, when only
		// interfaces that existed were listed at all - so absent means true.
		Present        *bool    `json:"present"`
		RxBytes        int64    `json:"rx_bytes"`
		TxBytes        int64    `json:"tx_bytes"`
		ProbeMS        *float64 `json:"probe_ms"`
		Error          string   `json:"error"`
		FallbackActive bool     `json:"fallback_active"`
		Peers          []struct {
			LastHandshakeAgeSeconds *float64 `json:"last_handshake_age_seconds"`
		} `json:"peers"`
	} `json:"tunnels"`
}

func toMonitoringHostDTO(name, address string, nodeID *int32, m *generated.HostMetric) monitoringHostDTO {
	dto := monitoringHostDTO{NodeID: nodeID, Name: name, Tunnels: []monitoringTunnelDTO{}}
	if address != "" {
		dto.Address = &address
	}
	if m == nil {
		return dto
	}
	collectedAt := m.CollectedAt.Time
	dto.CollectedAt = &collectedAt
	dto.Stale = time.Since(collectedAt) > staleAfter
	// reachable means "reporting healthily right now", matching the real
	// panel's `row.healthy and not stale`. Treating the mere existence of
	// an old sample as reachable meant a node whose collector died an hour
	// ago still showed as up, so every "node is down" alert keyed on this
	// field stopped firing entirely.
	dto.Reachable = m.Healthy && !dto.Stale
	dto.CPUPercent = pgFloat8ToPtr(m.CpuPercent)
	dto.MemPercent = pgFloat8ToPtr(m.MemPercent)
	dto.DiskPercent = pgFloat8ToPtr(m.DiskPercent)
	dto.RxRate = int8ToPtr(m.RxRate)
	dto.TxRate = int8ToPtr(m.TxRate)
	dto.Connections = pgInt4ToPtr(m.Connections)
	dto.ClientConns = pgInt4ToPtr(m.ClientConns)
	dto.TunnelsUp = pgInt4ToPtr(m.TunnelsUp)
	dto.TunnelsTotal = pgInt4ToPtr(m.TunnelsTotal)
	dto.Healthy = m.Healthy
	dto.HasMetrics = m.Payload.Valid && m.Payload.String != ""

	if dto.HasMetrics {
		var p hostMetricPayload
		if err := json.Unmarshal([]byte(m.Payload.String), &p); err == nil {
			uptime, load := p.UptimeSeconds, p.Load1m
			dto.Uptime, dto.Load1m = &uptime, &load
			running := p.XrayRunning
			dto.XrayRunning = &running
			if p.XrayVersion != "" {
				version := p.XrayVersion
				dto.XrayVersion = &version
			}
			sampledAt := p.CollectedAt
			if sampledAt.IsZero() {
				sampledAt = collectedAt
			}
			for _, t := range p.Tunnels {
				tunnel := monitoringTunnelDTO{
					Name: t.Name, Up: t.Up, Present: t.Present == nil || *t.Present,
					RxBytes: t.RxBytes, TxBytes: t.TxBytes,
					ProbeMS: t.ProbeMS, Error: t.Error, FallbackActive: t.FallbackActive,
				}
				// The freshest handshake of any peer is the tunnel's: one
				// stale peer among several says nothing about the path in use.
				for _, peer := range t.Peers {
					if peer.LastHandshakeAgeSeconds == nil {
						continue
					}
					age := math.Max(*peer.LastHandshakeAgeSeconds, 0)
					if tunnel.HandshakeAgeSeconds == nil || age < *tunnel.HandshakeAgeSeconds {
						tunnel.HandshakeAgeSeconds = &age
					}
				}
				if tunnel.HandshakeAgeSeconds != nil {
					tunnel.LastHandshake = sampledAt.Unix() - int64(math.Round(*tunnel.HandshakeAgeSeconds))
				}
				dto.Tunnels = append(dto.Tunnels, tunnel)
			}
		}
	}
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
