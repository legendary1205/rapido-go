package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/hostmetrics"
)

// onlineWindow mirrors the dashboard's own 180s "online" presence window
// (UsersTable's lastSeenOf) so the Overview hero stat and each row's
// presence dot always agree on what "online" means.
const onlineWindow = 180 * time.Second

// hostSampleTTL bounds how often GET /api/system may read this host
// directly - see cachedHostSample.
const hostSampleTTL = 30 * time.Second

// marzbanCompatVersion is a static, zero-cost compatibility value - external
// fleet-management bots (e.g. Mirza-bot-style panels, which "get_user" style
// tools like Guard resemble) probe this field before deciding which request
// shape to send (pre- vs post-Groups Marzban). Matches the real production
// panel's own currently-reported __version__ exactly (app/__init__.py),
// which such bots already treat as "legacy shape" successfully - not a
// version this rewrite is pinned to or will ever bump.
const marzbanCompatVersion = "0.8.4"

// systemStatsDTO mirrors app/models/system.py's SystemStats field for
// field. Every field is REQUIRED there, so a client that validates the
// response against that model (or simply indexes the key) breaks on any
// omission - the host-resource half used to be missing here entirely,
// which is every bot dashboard's CPU/RAM widget and every live-throughput
// readout. None of these carry omitempty for the same reason: a real zero
// must still appear on the wire.
type systemStatsDTO struct {
	Version                string  `json:"version"`
	MemTotal               int64   `json:"mem_total"`
	MemUsed                int64   `json:"mem_used"`
	CPUCores               int     `json:"cpu_cores"`
	CPUUsage               float64 `json:"cpu_usage"`
	TotalUser              int64   `json:"total_user"`
	OnlineUsers            int64   `json:"online_users"`
	UsersActive            int64   `json:"users_active"`
	UsersOnHold            int64   `json:"users_on_hold"`
	UsersDisabled          int64   `json:"users_disabled"`
	UsersExpired           int64   `json:"users_expired"`
	UsersLimited           int64   `json:"users_limited"`
	IncomingBandwidth      int64   `json:"incoming_bandwidth"`
	OutgoingBandwidth      int64   `json:"outgoing_bandwidth"`
	IncomingBandwidthSpeed int64   `json:"incoming_bandwidth_speed"`
	OutgoingBandwidthSpeed int64   `json:"outgoing_bandwidth_speed"`
}

// fillHostResources populates the CPU/RAM/throughput half of SystemStats.
//
// The absolute values (total/used bytes, core count) are read straight off
// this host, since they need no history to be correct. The two rates and
// the CPU percentage are derived quantities - they only exist as a delta
// between two samples - so they come from the panel's own stored
// self-sample (the node_id IS NULL row written by the backend's
// PanelSelfSampleLoop), the same numbers /api/monitoring shows for the
// "Panel" host. A panel running without that loop yet simply reports
// zeroes rather than omitting the fields.
func (h *Handler) fillHostResources(c *gin.Context, stats *systemStatsDTO) {
	latest, err := h.store.Queries.GetLatestHostMetricPerNode(c.Request.Context())
	if err != nil {
		return
	}
	for i := range latest {
		m := &latest[i]
		if m.NodeID.Valid {
			continue // a node's sample, not the panel's own
		}
		if m.CpuPercent.Valid {
			stats.CPUUsage = m.CpuPercent.Float64
		}
		if m.RxRate.Valid {
			stats.IncomingBandwidthSpeed = m.RxRate.Int64
		}
		if m.TxRate.Valid {
			stats.OutgoingBandwidthSpeed = m.TxRate.Int64
		}
		// The absolute figures live in the stored sample's own payload, so
		// serving them costs nothing beyond the read already being done.
		if m.Payload.Valid && m.Payload.String != "" {
			var p struct {
				CPUCores          int   `json:"cpu_cores"`
				MemTotalBytes     int64 `json:"mem_total_bytes"`
				MemAvailableBytes int64 `json:"mem_available_bytes"`
			}
			if err := json.Unmarshal([]byte(m.Payload.String), &p); err == nil {
				stats.CPUCores = p.CPUCores
				stats.MemTotal = p.MemTotalBytes
				if p.MemTotalBytes > 0 && p.MemAvailableBytes <= p.MemTotalBytes {
					stats.MemUsed = p.MemTotalBytes - p.MemAvailableBytes
				}
			}
		}
		if stats.MemTotal > 0 {
			return
		}
		break
	}

	// Only when no stored sample carries them (a panel whose backend
	// self-sample loop hasn't run yet) is the host read directly - and at
	// most once every 30s, because collecting walks /proc/net/tcp and
	// shells out to `wg`. Doing that per request made GET /api/system,
	// which the dashboard and every bot poll, one of the most expensive
	// calls on the panel.
	stats.MemTotal, stats.MemUsed, stats.CPUCores = h.cachedHostSample()
}

// cachedHostSample memoizes a direct host read for hostSampleTTL.
func (h *Handler) cachedHostSample() (memTotal, memUsed int64, cores int) {
	h.hostSampleMu.Lock()
	defer h.hostSampleMu.Unlock()
	if time.Since(h.hostSampleAt) < hostSampleTTL && h.hostSampleTotal > 0 {
		return h.hostSampleTotal, h.hostSampleUsed, h.hostSampleCores
	}
	s := hostmetrics.Collect(false, "")
	h.hostSampleAt = time.Now()
	h.hostSampleTotal, h.hostSampleCores = s.MemTotalBytes, s.CPUCores
	if s.MemTotalBytes > 0 && s.MemAvailableBytes <= s.MemTotalBytes {
		h.hostSampleUsed = s.MemTotalBytes - s.MemAvailableBytes
	}
	return h.hostSampleTotal, h.hostSampleUsed, h.hostSampleCores
}

// handleGetSystemStats implements GET /api/system, scoped like
// handleListUsers: sudo sees the whole fleet, a regular admin only their
// own users. IncomingBandwidth/OutgoingBandwidth come from the `system`
// singleton row, fleet-wide cumulative totals fed by every node's push
// report (see internal/httpapi/nodereport.go) - not scoped per-admin, same
// as the current Python system's own system.uplink/downlink.
func (h *Handler) handleGetSystemStats(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	ctx := c.Request.Context()

	var scopedAdminID pgtype.Int4
	if !identity.IsSudo {
		scopedAdminID = pgInt4FromInt(int(identity.AdminID))
	}

	total, err := h.store.Queries.CountUsers(ctx, generated.CountUsersParams{AdminID: scopedAdminID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read system stats"})
		return
	}
	byStatus, err := h.store.Queries.CountUsersByStatus(ctx, scopedAdminID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read system stats"})
		return
	}
	online, err := h.store.Queries.CountOnlineUsersSince(ctx, generated.CountOnlineUsersSinceParams{
		Cutoff: timestamptzFromTime(time.Now().UTC().Add(-onlineWindow)), AdminID: scopedAdminID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read system stats"})
		return
	}

	stats := systemStatsDTO{Version: marzbanCompatVersion, TotalUser: total, OnlineUsers: online}
	h.fillHostResources(c, &stats)
	if sys, err := h.store.Queries.GetSystem(ctx); err == nil {
		stats.IncomingBandwidth = pgInt8ToInt64(sys.Uplink)
		stats.OutgoingBandwidth = pgInt8ToInt64(sys.Downlink)
	}
	for _, row := range byStatus {
		switch row.Status {
		case statusActive:
			stats.UsersActive = row.Count
		case statusOnHold:
			stats.UsersOnHold = row.Count
		case statusDisabled:
			stats.UsersDisabled = row.Count
		case statusExpired:
			stats.UsersExpired = row.Count
		case statusLimited:
			stats.UsersLimited = row.Count
		}
	}
	c.JSON(http.StatusOK, stats)
}

type usagePointDTO struct {
	Date  string `json:"date"`
	Usage int64  `json:"usage"`
}

// handleGetSystemUsageHistory implements GET /api/system/usage-history,
// backed by node_usages (fed by every node's push report - see
// internal/httpapi/nodereport.go) via GetDailyUsageHistory, zero-filled
// for any day that query didn't return a row (a fleet day with zero
// traffic looks identical to a day with no node_usages rows at all).
func (h *Handler) handleGetSystemUsageHistory(c *gin.Context) {
	days := 14
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 365 {
			days = n
		}
	}
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -(days - 1))

	byDay := make(map[string]int64, days)
	rows, err := h.store.Queries.GetDailyUsageHistory(c.Request.Context(), timestamptzFromTime(start.Truncate(24*time.Hour)))
	if err == nil {
		for _, r := range rows {
			byDay[r.Day.Time.Format("2006-01-02")] = r.Usage
		}
	}

	out := make([]usagePointDTO, 0, days)
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		out = append(out, usagePointDTO{Date: date, Usage: byDay[date]})
	}
	c.JSON(http.StatusOK, out)
}
