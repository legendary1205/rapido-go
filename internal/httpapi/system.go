package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// onlineWindow mirrors the dashboard's own 180s "online" presence window
// (UsersTable's lastSeenOf) so the Overview hero stat and each row's
// presence dot always agree on what "online" means.
const onlineWindow = 180 * time.Second

type systemStatsDTO struct {
	TotalUser         int64 `json:"total_user"`
	OnlineUsers       int64 `json:"online_users"`
	UsersActive       int64 `json:"users_active"`
	UsersOnHold       int64 `json:"users_on_hold"`
	UsersDisabled     int64 `json:"users_disabled"`
	UsersExpired      int64 `json:"users_expired"`
	UsersLimited      int64 `json:"users_limited"`
	IncomingBandwidth int64 `json:"incoming_bandwidth"`
	OutgoingBandwidth int64 `json:"outgoing_bandwidth"`
}

// handleGetSystemStats implements GET /api/system, scoped like
// handleListUsers: sudo sees the whole fleet, a regular admin only their
// own users. IncomingBandwidth/OutgoingBandwidth are always 0 - there is no
// usage-reporting pipeline from nodes yet (a real, already-known gap, not
// an oversight here), so this returns honest zeros rather than fabricated
// numbers until that pipeline exists in a later phase.
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

	stats := systemStatsDTO{TotalUser: total, OnlineUsers: online}
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

// handleGetSystemUsageHistory implements GET /api/system/usage-history.
// Makes no DB query at all: there is no historical usage-tracking pipeline
// yet (nothing increments users.used_traffic over time), so this returns
// real calendar dates with an honest usage:0 for each, rather than querying
// a table that can't yet answer the question.
func (h *Handler) handleGetSystemUsageHistory(c *gin.Context) {
	days := 14
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 365 {
			days = n
		}
	}
	c.JSON(http.StatusOK, buildEmptyUsageHistory(days, time.Now().UTC()))
}

func buildEmptyUsageHistory(days int, now time.Time) []usagePointDTO {
	out := make([]usagePointDTO, 0, days)
	start := now.AddDate(0, 0, -(days - 1))
	for i := 0; i < days; i++ {
		out = append(out, usagePointDTO{Date: start.AddDate(0, 0, i).Format("2006-01-02"), Usage: 0})
	}
	return out
}
