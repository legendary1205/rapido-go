package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// handleResetAllUsersUsage implements POST /api/users/reset (sudo only) -
// mirrors crud.reset_all_users_data_usage: zero every one of the caller's
// own users' counters, wipe their usage history, and drop any pending next
// plan. Scoped to the caller's own users exactly as the real panel does
// (it passes its own admin row as the filter), with the one exception the
// real panel also has: the env-bootstrap sudo account owns no admins row,
// so for it the filter is empty and the reset is fleet-wide.
func (h *Handler) handleResetAllUsersUsage(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	ctx := c.Request.Context()

	var ids []int32
	var err error
	if identity.AdminID == 0 {
		ids, err = h.store.Queries.ListAllUserIDs(ctx)
	} else {
		ids, err = h.store.Queries.ListUserIDsByAdmin(ctx, pgtype.Int4{Int32: identity.AdminID, Valid: true})
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read users"})
		return
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"detail": "Users successfully reset."})
		return
	}

	if err := h.store.Queries.ResetUsersUsageByIDs(ctx, ids); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not reset users"})
		return
	}
	if err := h.store.Queries.DeleteNodeUserUsagesByUserIDs(ctx, ids); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not clear usage history"})
		return
	}
	if err := h.store.Queries.DeleteUserUsageLogsByUserIDs(ctx, ids); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not clear usage logs"})
		return
	}
	if err := h.store.Queries.DeleteNextPlansByUserIDs(ctx, ids); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not clear next plans"})
		return
	}
	// A status flip from limited back to active changes what every node
	// should be serving, so the fleet-wide config has to be rebuilt rather
	// than left to the cache's own TTL.
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Users successfully reset."})
}

// expiredUsersInWindow resolves the ?expired_after=/?expired_before= pair
// both /api/users/expired verbs share. Defaults match
// get_expired_users_list exactly: no lower bound, upper bound "now".
func (h *Handler) expiredUsersInWindow(c *gin.Context) ([]generated.ListExpiredUsersRow, bool) {
	after := int32(0)
	before := int32(time.Now().UTC().Unix())

	if raw := c.Query("expired_after"); raw != "" {
		t, err := parseISOTime(raw)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Invalid expired_after date"})
			return nil, false
		}
		after = int32(t.Unix())
	}
	if raw := c.Query("expired_before"); raw != "" {
		t, err := parseISOTime(raw)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Invalid expired_before date"})
			return nil, false
		}
		before = int32(t.Unix())
	}
	if before < after {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Start date must be before end date"})
		return nil, false
	}

	identity := auth.CurrentIdentity(c)
	ctx := c.Request.Context()

	if identity.IsSudo {
		rows, err := h.store.Queries.ListExpiredUsers(ctx, generated.ListExpiredUsersParams{
			Expire:   pgtype.Int4{Int32: after, Valid: true},
			Expire_2: pgtype.Int4{Int32: before, Valid: true},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read users"})
			return nil, false
		}
		return rows, true
	}

	scoped, err := h.store.Queries.ListExpiredUsersByAdmin(ctx, generated.ListExpiredUsersByAdminParams{
		Expire:   pgtype.Int4{Int32: after, Valid: true},
		Expire_2: pgtype.Int4{Int32: before, Valid: true},
		AdminID:  pgtype.Int4{Int32: identity.AdminID, Valid: true},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read users"})
		return nil, false
	}
	rows := make([]generated.ListExpiredUsersRow, 0, len(scoped))
	for _, r := range scoped {
		rows = append(rows, generated.ListExpiredUsersRow{ID: r.ID, Username: r.Username, AdminID: r.AdminID})
	}
	return rows, true
}

// handleGetExpiredUsers implements GET /api/users/expired - a bare JSON
// array of usernames, matching response_model=List[str].
func (h *Handler) handleGetExpiredUsers(c *gin.Context) {
	rows, ok := h.expiredUsersInWindow(c)
	if !ok {
		return
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Username)
	}
	c.JSON(http.StatusOK, names)
}

// handleDeleteExpiredUsers implements DELETE /api/users/expired. Returns
// the usernames it removed; an empty match is a 404 rather than an empty
// list, matching the real panel (a caller that meant to clean up gets told
// nothing matched instead of silently believing it worked).
func (h *Handler) handleDeleteExpiredUsers(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	rows, ok := h.expiredUsersInWindow(c)
	if !ok {
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"detail": "No expired users found in the specified date range"})
		return
	}

	ctx := c.Request.Context()
	ids := make([]int32, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if err := h.store.Queries.DeleteUsersByIDs(ctx, ids); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not delete users"})
		return
	}

	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Username)
		h.reports.UserDeleted(ctx, r.Username, identity.Username, h.resolveAdminRef(ctx, r.AdminID))
	}
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	c.JSON(http.StatusOK, names)
}

// handleSetUserOwner implements PUT /api/user/:username/set-owner (sudo
// only) - the new owner arrives as the ?admin_username= query parameter,
// not a body, exactly as the real panel declares it.
func (h *Handler) handleSetUserOwner(c *gin.Context) {
	dbuser, ok := h.loadAuthorizedUser(c)
	if !ok {
		return
	}
	adminUsername := c.Query("admin_username")
	if adminUsername == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "admin_username is required"})
		return
	}
	ctx := c.Request.Context()
	newAdmin, err := h.store.Queries.GetAdminByUsername(ctx, adminUsername)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Admin not found"})
		return
	}

	updated, err := h.store.Queries.SetUserOwner(ctx, generated.SetUserOwnerParams{
		ID: dbuser.ID, AdminID: pgtype.Int4{Int32: newAdmin.ID, Valid: true},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not set owner"})
		return
	}
	resp, err := h.buildUserResponse(ctx, updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read user"})
		return
	}
	h.attachUserLinks(ctx, updated, &resp)
	c.JSON(http.StatusOK, resp)
}

// handleActivateNextPlan implements POST /api/user/:username/active-next -
// fires a user's pending next plan immediately instead of waiting for the
// review job to notice a limit was crossed. Same four steps
// internal/reviewjob's own fireNextPlan takes (log the pre-reset usage,
// clear per-node history, apply the plan's formula, drop the plan).
func (h *Handler) handleActivateNextPlan(c *gin.Context) {
	dbuser, ok := h.loadAuthorizedUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	if _, err := h.store.Queries.GetNextPlanByUserID(ctx, dbuser.ID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "User doesn't have next plan"})
		return
	}

	if err := h.store.Queries.CreateUserUsageLog(ctx, generated.CreateUserUsageLogParams{
		UserID: pgtype.Int4{Int32: dbuser.ID, Valid: true}, UsedTrafficAtReset: dbuser.UsedTraffic,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not log usage"})
		return
	}
	if err := h.store.Queries.ClearNodeUserUsages(ctx, pgtype.Int4{Int32: dbuser.ID, Valid: true}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not clear usage history"})
		return
	}
	updated, err := h.store.Queries.ResetUserByNextPlan(ctx, dbuser.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not activate next plan"})
		return
	}
	if err := h.store.Queries.DeleteNextPlanByUserID(ctx, dbuser.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not clear next plan"})
		return
	}

	resp, err := h.buildUserResponse(ctx, updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read user"})
		return
	}
	h.reports.UserDataResetByNext(ctx, toUserSummary(resp), h.resolveAdminRef(ctx, updated.AdminID))
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		h.logger.Warn("invalidate node config cache", "error", err)
	}
	h.attachUserLinks(ctx, updated, &resp)
	c.JSON(http.StatusOK, resp)
}
