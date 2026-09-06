package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// handleSubscriptionInfo implements GET /sub/:token/info: the same shape
// the HTML subscription page's Overview tab embeds at load, re-fetched by
// its 60s background poll. Mirrors app/routers/subscription.py's
// user_subscription_info, which just returns the validated user as-is.
func (h *Handler) handleSubscriptionInfo(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}
	data, err := h.buildRapidoData(c.Request.Context(), user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read subscription info"})
		return
	}
	c.JSON(http.StatusOK, data)
}

const (
	emergencyRechargeDataBytes = 50 * 1024 * 1024 // 50 MB, matches crud.EMERGENCY_RECHARGE_DATA
	emergencyRechargeSeconds   = 30 * 60          // 30 minutes, matches crud.EMERGENCY_RECHARGE_SECONDS
)

// handleEmergencyRecharge implements POST /sub/:token/emergency: a
// one-time, race-safe self-service grant of +50MB/+30min for a user who
// ran dry, reactivating their account immediately. Mirrors
// app/routers/subscription.py's user_emergency_recharge.
func (h *Handler) handleEmergencyRecharge(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}
	if user.EmergencyUsedAt.Valid {
		c.Header("X-Error-Code", "emergency_used")
		c.JSON(http.StatusConflict, gin.H{"detail": "Emergency recharge has already been used"})
		return
	}
	// The subscription page greys the button out, but the client is never
	// trusted: only an actually out-of-data/out-of-time user may self-reactivate.
	if user.Status != statusLimited && user.Status != statusExpired {
		c.Header("X-Error-Code", "emergency_not_eligible")
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Emergency recharge is only available when your account is out of data or expired"})
		return
	}

	updated, err := h.store.Queries.ApplyEmergencyRecharge(c.Request.Context(), user.ID)
	if err != nil {
		// Lost the race against a concurrent request that claimed the grant
		// first (zero rows matched WHERE ... AND emergency_used_at IS NULL) -
		// pgx surfaces that as ErrNoRows on a :one query, same signal as
		// Python's rowcount==0 path.
		c.Header("X-Error-Code", "emergency_used")
		c.JSON(http.StatusConflict, gin.H{"detail": "Emergency recharge has already been used"})
		return
	}

	h.logger.Info("emergency recharge used", "username", updated.Username)
	c.JSON(http.StatusOK, gin.H{
		"username":          updated.Username,
		"status":            updated.Status,
		"data_limit":        int8ToPtr(updated.DataLimit),
		"used_traffic":      updated.UsedTraffic,
		"expire":            pgInt4ToPtrInt64(updated.Expire),
		"emergency_used_at": timestamptzToPtr(updated.EmergencyUsedAt),
		"granted_data":      emergencyRechargeDataBytes,
		"granted_seconds":   emergencyRechargeSeconds,
	})
}
