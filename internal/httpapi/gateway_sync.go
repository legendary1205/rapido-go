package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/gatewayclient"
)

// gatewaySyncPayload is what POST /api/internal/gateway/users/sync
// receives - a full snapshot of one user's identity/policy/credentials,
// not a diff, pushed by the panel that actually owns the user. Only what
// a peer needs to authenticate and enforce policy for this user on its
// OWN nodes: no note, no on_hold_*/auto_delete_in_days, no next_plan -
// those are local-panel-only admin concerns this feature has no reason to
// mirror. Field-for-field the same shape as gatewayclient.UserSyncPayload
// (that package can't import this one - see its own doc comment on why
// the two aren't literally the same Go type).
type gatewaySyncPayload struct {
	OriginPanelName        string                     `json:"origin_panel_name"`
	Username               string                     `json:"username" binding:"required"`
	Deleted                bool                       `json:"deleted"`
	Status                 string                     `json:"status"`
	DataLimit              *int64                     `json:"data_limit"`
	DataLimitResetStrategy string                     `json:"data_limit_reset_strategy"`
	Expire                 *int64                     `json:"expire"`
	Proxies                map[string]json.RawMessage `json:"proxies"`
}

// handleGatewaySyncUser implements POST /api/internal/gateway/users/sync
// (panel-to-panel, requireGatewaySecret). Upserts a REPLICA by username -
// never touches a genuine local user (one with synced_from_panel_name
// still NULL): a same-username collision is a real admin misconfiguration
// (both panels independently have their own real user with this name) and
// is reported as a conflict, not silently resolved either way.
func (h *Handler) handleGatewaySyncUser(c *gin.Context) {
	var req gatewaySyncPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"ok": false, "detail": err.Error()})
		return
	}
	ctx := c.Request.Context()

	existing, err := h.store.Queries.GetUserByUsername(ctx, req.Username)
	found := true
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "detail": err.Error()})
			return
		}
		found = false
	}

	if req.Deleted {
		if !found {
			c.JSON(http.StatusOK, gin.H{"ok": true}) // already gone - idempotent
			return
		}
		if !existing.SyncedFromPanelName.Valid {
			c.JSON(http.StatusConflict, gin.H{"ok": false, "detail": "a real local user with this username exists here - refusing to delete it"})
			return
		}
		if err := h.store.Queries.DeleteUser(ctx, existing.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "detail": "could not delete replica: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}

	if found && !existing.SyncedFromPanelName.Valid {
		c.JSON(http.StatusConflict, gin.H{"ok": false, "detail": "a real local user with this username already exists here - not a replica, refusing to overwrite it"})
		return
	}

	settingsByType, err := resolveProxySettings(req.Proxies)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"ok": false, "detail": err.Error()})
		return
	}

	var userRow generated.User
	if found {
		userRow, err = h.store.Queries.UpdateGatewaySyncedUser(ctx, generated.UpdateGatewaySyncedUserParams{
			ID: existing.ID, Status: req.Status, DataLimit: int8FromPtr(req.DataLimit),
			DataLimitResetStrategy: strOr(&req.DataLimitResetStrategy, "no_reset"),
			Expire:                 pgInt4FromPtrInt64(req.Expire),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "detail": "could not update replica: " + err.Error()})
			return
		}
		if err := h.reconcileProxies(ctx, userRow.ID, settingsByType); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "detail": err.Error()})
			return
		}
	} else {
		userRow, err = h.store.Queries.CreateGatewaySyncedUser(ctx, generated.CreateGatewaySyncedUserParams{
			Username: req.Username, Status: req.Status, DataLimit: int8FromPtr(req.DataLimit),
			DataLimitResetStrategy: strOr(&req.DataLimitResetStrategy, "no_reset"),
			Expire:                 pgInt4FromPtrInt64(req.Expire),
			SyncedFromPanelName:    textFromPtr(&req.OriginPanelName),
		})
		if err != nil {
			if isUniqueViolation(err) {
				c.JSON(http.StatusConflict, gin.H{"ok": false, "detail": "a real local user with this username already exists here"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "detail": "could not create replica: " + err.Error()})
			return
		}
		// A brand-new replica defaults to full access on every one of THIS
		// panel's own inbounds for each synced protocol, same as a real new
		// user - this panel's own admin can restrict it locally afterward
		// exactly like any other user, see createProxyForUser's own doc
		// comment.
		for protocol, settings := range settingsByType {
			known, err := h.store.CachedListInboundTagsByProtocol(ctx, protocol)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "detail": err.Error()})
				return
			}
			if err := h.createProxyForUser(ctx, userRow.ID, protocol, settings, known); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "detail": err.Error()})
				return
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// --- outbound dispatch ------------------------------------------------

// gatewaySyncTimeout bounds one peer call - a slow or hung peer must never
// hold this goroutine (or, transitively, delay the NEXT admin action that
// happens to reuse the same underlying connection pool) indefinitely.
const gatewaySyncTimeout = 10 * time.Second

// dispatchGatewayUserSync fires a fire-and-forget sync push to every
// enabled peer after a LOCAL user create/update succeeds - never called
// for a replica (synced_from_panel_name already set): re-syncing a
// replica back out would ping-pong it across every peer forever. Errors
// are logged loudly, never swallowed (see internal/gatewayclient's own
// doc comment on why this project treats silent failure here as a real
// bug class, not acceptable resilience) - but never block or fail the
// admin's own request, which has already succeeded locally by the time
// this runs.
func (h *Handler) dispatchGatewayUserSync(ctx context.Context, user generated.User) {
	if user.SyncedFromPanelName.Valid {
		return
	}
	proxies, err := h.store.Queries.ListProxiesByUserID(ctx, pgInt4FromInt(int(user.ID)))
	if err != nil {
		h.logger.Warn("gateway sync: could not read proxies, skipping", "user", user.Username, "error", err)
		return
	}
	proxyPayload := make(map[string]json.RawMessage, len(proxies))
	for _, p := range proxies {
		proxyPayload[p.Type] = p.Settings
	}

	var expire *int64
	if user.Expire.Valid {
		v := int64(user.Expire.Int32)
		expire = &v
	}
	var dataLimit *int64
	if user.DataLimit.Valid {
		dataLimit = &user.DataLimit.Int64
	}

	payload := gatewayclient.UserSyncPayload{
		Username: user.Username, Status: user.Status, DataLimit: dataLimit,
		DataLimitResetStrategy: user.DataLimitResetStrategy, Expire: expire, Proxies: proxyPayload,
	}
	h.dispatchGatewayPush(ctx, payload)
}

// dispatchGatewayUserDelete mirrors dispatchGatewayUserSync for a LOCAL
// user's deletion - same replica guard, same fire-and-forget shape.
func (h *Handler) dispatchGatewayUserDelete(ctx context.Context, user generated.User) {
	if user.SyncedFromPanelName.Valid {
		return
	}
	h.dispatchGatewayPush(ctx, gatewayclient.UserSyncPayload{Username: user.Username, Deleted: true})
}

func (h *Handler) dispatchGatewayPush(ctx context.Context, payload gatewayclient.UserSyncPayload) {
	peers, err := h.store.Queries.ListGatewayPeers(ctx)
	if err != nil {
		h.logger.Warn("gateway sync: could not list peers, skipping", "error", err)
		return
	}
	if len(peers) == 0 {
		return
	}
	settings, err := h.ensureGatewaySettings(ctx)
	if err != nil {
		h.logger.Warn("gateway sync: could not load this panel's own settings, skipping", "error", err)
		return
	}
	payload.OriginPanelName = settings.Name

	for _, peer := range peers {
		if !peer.Enabled {
			continue
		}
		peer := peer
		go func() {
			callCtx, cancel := context.WithTimeout(context.Background(), gatewaySyncTimeout)
			defer cancel()
			if _, err := gatewayclient.SyncUser(callCtx, peer.BaseUrl, peer.Secret, payload); err != nil {
				h.logger.Warn("gateway sync: peer rejected or was unreachable",
					"peer", peer.Name, "user", payload.Username, "deleted", payload.Deleted, "error", err)
			}
		}()
	}
}
