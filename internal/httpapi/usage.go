package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// userUsageDTO mirrors app/models/user.py's UserUsageResponse field for
// field - node_id is deliberately nullable, and a null one means the
// panel's own core rather than any node (the "Master" row every one of
// these responses leads with).
type userUsageDTO struct {
	NodeID      *int32 `json:"node_id"`
	NodeName    string `json:"node_name"`
	UsedTraffic int64  `json:"used_traffic"`
}

// usageWindow resolves the ?start=/?end= pair the real panel's
// validate_dates dependency defines: ISO-8601 values, defaulting to the
// last 30 days, with an explicitly inverted range rejected as a 400 rather
// than silently returning nothing.
func usageWindow(c *gin.Context) (start, end time.Time, ok bool) {
	now := time.Now().UTC()
	start, end = now.AddDate(0, 0, -30), now

	if raw := c.Query("start"); raw != "" {
		t, err := parseISOTime(raw)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Invalid start date"})
			return time.Time{}, time.Time{}, false
		}
		start = t
	}
	if raw := c.Query("end"); raw != "" {
		t, err := parseISOTime(raw)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Invalid end date"})
			return time.Time{}, time.Time{}, false
		}
		end = t
	}
	if end.Before(start) {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Start date must be before end date"})
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// parseISOTime accepts the same spellings Python's datetime.fromisoformat
// takes from a query string - with or without an explicit zone, and with
// or without seconds - treating a zone-less value as UTC (what
// validate_dates does via .astimezone(timezone.utc)).
func parseISOTime(raw string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range layouts {
		t, err := time.Parse(layout, raw)
		if err == nil {
			return t.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

// usageAccumulator builds the fixed "Master first, then every node, all
// starting at zero" skeleton both usage endpoints return, so a node that
// moved no traffic in the window still appears with a 0 instead of
// vanishing from the response (the real panel's own behaviour, and what
// makes a client's per-node chart keep a stable set of series).
type usageAccumulator struct {
	order   []int32 // 0 = Master
	byNode  map[int32]*userUsageDTO
}

func (h *Handler) newUsageAccumulator(ctx context.Context) (*usageAccumulator, error) {
	acc := &usageAccumulator{byNode: map[int32]*userUsageDTO{}}
	acc.byNode[0] = &userUsageDTO{NodeID: nil, NodeName: "Master"}
	acc.order = append(acc.order, 0)

	nodes, err := h.store.Queries.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		id := n.ID
		acc.byNode[id] = &userUsageDTO{NodeID: &id, NodeName: n.Name}
		acc.order = append(acc.order, id)
	}
	return acc, nil
}

// add folds one summed row in. A row whose node_id is NULL belongs to the
// panel's own core (Master); one naming a node that has since been deleted
// is dropped rather than inventing a series for it.
func (a *usageAccumulator) add(nodeID pgtype.Int4, total int64) {
	key := int32(0)
	if nodeID.Valid {
		key = nodeID.Int32
	}
	if entry, ok := a.byNode[key]; ok {
		entry.UsedTraffic += total
	}
}

func (a *usageAccumulator) list() []userUsageDTO {
	out := make([]userUsageDTO, 0, len(a.order))
	for _, id := range a.order {
		out = append(out, *a.byNode[id])
	}
	return out
}

// buildUserUsages is the shared body of GET /api/user/:username/usage and
// GET /sub/:token/usage - both return the identical shape on the real
// panel, from the identical crud.get_user_usages call.
func (h *Handler) buildUserUsages(ctx context.Context, userID int32, start, end time.Time) ([]userUsageDTO, error) {
	acc, err := h.newUsageAccumulator(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := h.store.Queries.SumUserUsageByNode(ctx, generated.SumUserUsageByNodeParams{
		UserID:      pgtype.Int4{Int32: userID, Valid: true},
		CreatedAt:   pgtype.Timestamptz{Time: start, Valid: true},
		CreatedAt_2: pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		acc.add(r.NodeID, r.Total)
	}
	return acc.list(), nil
}

// handleGetUserUsage implements GET /api/user/:username/usage.
func (h *Handler) handleGetUserUsage(c *gin.Context) {
	dbuser, ok := h.loadAuthorizedUser(c)
	if !ok {
		return
	}
	start, end, ok := usageWindow(c)
	if !ok {
		return
	}
	usages, err := h.buildUserUsages(c.Request.Context(), dbuser.ID, start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read usage"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"username": dbuser.Username, "usages": usages})
}

// handleGetSubscriptionUsage implements GET /sub/:token/usage - the same
// payload as the admin-facing endpoint above, authenticated by the
// subscription token instead of an admin JWT.
func (h *Handler) handleGetSubscriptionUsage(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}
	start, end, ok := usageWindow(c)
	if !ok {
		return
	}
	usages, err := h.buildUserUsages(c.Request.Context(), user.ID, start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read usage"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"username": user.Username, "usages": usages})
}

// handleGetUsersUsage implements GET /api/users/usage - fleet-wide totals
// for a sudo admin, and only the caller's own users' traffic otherwise
// (the same scoping the real panel applies by passing [admin.username] as
// the owner filter for a non-sudo caller).
func (h *Handler) handleGetUsersUsage(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	start, end, ok := usageWindow(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	acc, err := h.newUsageAccumulator(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read usage"})
		return
	}

	if identity.IsSudo {
		// ?admin=alice&admin=bob narrows fleet-wide totals to those
		// resellers' users - the real panel's own filter (declared there as
		// `owner` with alias "admin"), and the whole point of this endpoint
		// for a per-reseller billing client. An unknown name simply matches
		// no users, yielding all-zero usages with a 200, exactly as it does
		// there.
		if owners := c.QueryArray("admin"); len(owners) > 0 {
			adminIDs := make([]int32, 0, len(owners))
			for _, name := range owners {
				admin, err := h.store.Queries.GetAdminByUsername(ctx, name)
				if err != nil {
					continue
				}
				adminIDs = append(adminIDs, admin.ID)
			}
			if len(adminIDs) > 0 {
				rows, err := h.store.Queries.SumAllUsersUsageByNodeForAdmins(ctx, generated.SumAllUsersUsageByNodeForAdminsParams{
					CreatedAt:   pgtype.Timestamptz{Time: start, Valid: true},
					CreatedAt_2: pgtype.Timestamptz{Time: end, Valid: true},
					AdminIds:    adminIDs,
				})
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read usage"})
					return
				}
				for _, r := range rows {
					acc.add(r.NodeID, r.Total)
				}
			}
			c.JSON(http.StatusOK, gin.H{"usages": acc.list()})
			return
		}

		rows, err := h.store.Queries.SumAllUsersUsageByNode(ctx, generated.SumAllUsersUsageByNodeParams{
			CreatedAt:   pgtype.Timestamptz{Time: start, Valid: true},
			CreatedAt_2: pgtype.Timestamptz{Time: end, Valid: true},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read usage"})
			return
		}
		for _, r := range rows {
			acc.add(r.NodeID, r.Total)
		}
	} else {
		rows, err := h.store.Queries.SumAllUsersUsageByNodeForAdmin(ctx, generated.SumAllUsersUsageByNodeForAdminParams{
			CreatedAt:   pgtype.Timestamptz{Time: start, Valid: true},
			CreatedAt_2: pgtype.Timestamptz{Time: end, Valid: true},
			AdminID:     pgtype.Int4{Int32: identity.AdminID, Valid: true},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read usage"})
			return
		}
		for _, r := range rows {
			acc.add(r.NodeID, r.Total)
		}
	}

	c.JSON(http.StatusOK, gin.H{"usages": acc.list()})
}

// handleGetAdminUsage implements GET /api/admin/usage/:username - a bare
// integer body, exactly as the real panel returns (response_model=int).
func (h *Handler) handleGetAdminUsage(c *gin.Context) {
	admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), c.Param("username"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Admin not found"})
		return
	}
	c.JSON(http.StatusOK, admin.UsersUsage)
}
