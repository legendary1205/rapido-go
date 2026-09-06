package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/proxysettings"
	"github.com/legendary1205/rapido-go/internal/subscription"
)

// allowedUsernameChars mirrors the character class in app/models/user.py's
// USERNAME_REGEXP (`[a-zA-Z0-9-_@.]`). The full Python pattern also opens
// with a `(?=\w{3,32}\b)` lookahead, which Go's RE2 engine can't express -
// validUsername below reproduces its actual effect (the leading run of
// \w = [A-Za-z0-9_] characters must be 3-32 long) with a plain scan instead.
var allowedUsernameChars = regexp.MustCompile(`^[a-zA-Z0-9_@.-]+$`)

func validUsername(s string) bool {
	if !allowedUsernameChars.MatchString(s) {
		return false
	}
	leadingWordRun := 0
	for _, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			leadingWordRun++
		} else {
			break
		}
	}
	return leadingWordRun >= 3 && leadingWordRun <= 32
}

const (
	statusActive   = "active"
	statusDisabled = "disabled"
	statusLimited  = "limited"
	statusExpired  = "expired"
	statusOnHold   = "on_hold"
)

var validCreateStatus = map[string]bool{statusActive: true, statusOnHold: true}
var validModifyStatus = map[string]bool{statusActive: true, statusDisabled: true, statusOnHold: true}
var validResetStrategy = map[string]bool{"no_reset": true, "day": true, "week": true, "month": true, "year": true}

type nextPlanDTO struct {
	DataLimit           int64 `json:"data_limit"`
	Expire              int64 `json:"expire"`
	AddRemainingTraffic bool  `json:"add_remaining_traffic"`
	FireOnEither        bool  `json:"fire_on_either"`
}

type userWriteRequest struct {
	Status                 *string                    `json:"status"`
	Proxies                map[string]json.RawMessage `json:"proxies"`
	Inbounds               map[string][]string        `json:"inbounds"`
	Expire                 *int64                     `json:"expire"`
	DataLimit              *int64                     `json:"data_limit"`
	DataLimitResetStrategy *string                    `json:"data_limit_reset_strategy"`
	Note                   *string                    `json:"note"`
	OnHoldExpireDuration   *int64                     `json:"on_hold_expire_duration"`
	OnHoldTimeout          *time.Time                 `json:"on_hold_timeout"`
	AutoDeleteInDays       *int32                     `json:"auto_delete_in_days"`
	NextPlan               *nextPlanDTO               `json:"next_plan"`
}

type userCreateRequest struct {
	Username string `json:"username" binding:"required"`
	userWriteRequest
}

type userResponseDTO struct {
	ID                     int32                      `json:"id"`
	Username               string                     `json:"username"`
	Status                 string                     `json:"status"`
	UsedTraffic            int64                      `json:"used_traffic"`
	LifetimeUsedTraffic    int64                      `json:"lifetime_used_traffic"`
	DataLimit              *int64                     `json:"data_limit"`
	DataLimitResetStrategy string                     `json:"data_limit_reset_strategy"`
	Expire                 *int64                     `json:"expire"`
	Note                   *string                    `json:"note"`
	CreatedAt              time.Time                  `json:"created_at"`
	OnHoldExpireDuration   *int64                     `json:"on_hold_expire_duration"`
	OnHoldTimeout          *time.Time                 `json:"on_hold_timeout"`
	AutoDeleteInDays       *int32                     `json:"auto_delete_in_days"`
	AdminUsername          *string                    `json:"admin_username"`
	Proxies                map[string]json.RawMessage `json:"proxies"`
	Inbounds               map[string][]string        `json:"inbounds"`
	ExcludedInbounds       map[string][]string        `json:"excluded_inbounds"`
	NextPlan               *nextPlanDTO               `json:"next_plan"`
	SubscriptionURL        string                     `json:"subscription_url"`
}

// handleCreateUser implements POST /api/user.
func (h *Handler) handleCreateUser(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	var body userCreateRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	username, req := body.Username, body.userWriteRequest

	if !validUsername(username) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Username only can be 3 to 32 characters and contain a-z, 0-9, and underscores in between."})
		return
	}
	if req.Note != nil && len(*req.Note) > 500 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "User's note can be a maximum of 500 character"})
		return
	}
	if len(req.Proxies) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Each user needs at least one proxy"})
		return
	}

	status := statusActive
	if req.Status != nil && *req.Status != "" {
		if !validCreateStatus[*req.Status] {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid status for user creation"})
			return
		}
		status = *req.Status
	}
	onHoldExpire := normalizeZero(req.OnHoldExpireDuration)
	if status == statusOnHold {
		if onHoldExpire == nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "User cannot be on hold without a valid on_hold_expire_duration."})
			return
		}
		if normalizeZero(req.Expire) != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "User cannot be on hold with specified expire."})
			return
		}
	}
	if req.DataLimitResetStrategy != nil && !validResetStrategy[*req.DataLimitResetStrategy] {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid data_limit_reset_strategy"})
		return
	}

	ctx := c.Request.Context()
	settingsByType, err := resolveProxySettings(req.Proxies)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}

	// UserCreate.validate_inbounds: unknown protocols in `inbounds` are
	// dropped, and a protocol with no tags given is auto-filled with every
	// known inbound for that protocol (see app/models/user.py:170-197).
	resolvedInbounds := map[string][]string{}
	for protocol := range settingsByType {
		tags := req.Inbounds[protocol]
		if len(tags) > 0 {
			for _, tag := range tags {
				if _, err := h.store.Queries.GetInboundByTag(ctx, tag); err != nil {
					c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": fmt.Sprintf("Inbound %s doesn't exist", tag)})
					return
				}
			}
			resolvedInbounds[protocol] = tags
		} else {
			known, err := h.store.Queries.ListInboundTagsByProtocol(ctx, protocol)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not resolve inbounds"})
				return
			}
			resolvedInbounds[protocol] = known
		}
	}

	var adminID pgtype.Int4
	if identity.AdminID != 0 {
		adminID = pgInt4FromInt(int(identity.AdminID))
	}

	dbuser, err := h.store.Queries.CreateUser(ctx, generated.CreateUserParams{
		Username:               username,
		Status:                 status,
		DataLimit:              int8FromPtr(normalizeZero(req.DataLimit)),
		DataLimitResetStrategy: strOr(req.DataLimitResetStrategy, "no_reset"),
		Expire:                 pgInt4FromPtrInt64(normalizeZero(req.Expire)),
		AdminID:                adminID,
		Note:                   textFromPtr(req.Note),
		OnHoldExpireDuration:   int8FromPtr(onHoldExpire),
		OnHoldTimeout:          timestamptzFromPtrTime(req.OnHoldTimeout),
		AutoDeleteInDays:       pgInt4FromPtr(req.AutoDeleteInDays),
	})
	if err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"detail": "User already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create user"})
		return
	}

	for protocol, settings := range settingsByType {
		if err := h.createProxyForUser(ctx, dbuser.ID, protocol, settings, resolvedInbounds[protocol]); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
			return
		}
	}

	if req.NextPlan != nil {
		if _, err := h.store.Queries.UpsertNextPlan(ctx, generated.UpsertNextPlanParams{
			UserID: dbuser.ID, DataLimit: req.NextPlan.DataLimit, Expire: pgInt4FromInt(int(req.NextPlan.Expire)),
			AddRemainingTraffic: req.NextPlan.AddRemainingTraffic, FireOnEither: req.NextPlan.FireOnEither,
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create next_plan"})
			return
		}
	}

	resp, err := h.buildUserResponse(ctx, dbuser)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "user created but could not be read back"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// createProxyForUser inserts one Proxy row plus its excluded_inbounds
// (known tags for that protocol minus the ones the user is allowed).
func (h *Handler) createProxyForUser(ctx context.Context, userID int32, protocol string, settings proxysettings.Settings, includedTags []string) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	proxy, err := h.store.Queries.CreateProxy(ctx, generated.CreateProxyParams{
		UserID: pgInt4FromInt(int(userID)), Type: protocol, Settings: raw,
	})
	if err != nil {
		return fmt.Errorf("could not create %s proxy", protocol)
	}
	known, err := h.store.Queries.ListInboundTagsByProtocol(ctx, protocol)
	if err != nil {
		return err
	}
	excluded := subtractTags(known, includedTags)
	if len(excluded) > 0 {
		if err := h.store.Queries.ReplaceExcludedInbounds(ctx, generated.ReplaceExcludedInboundsParams{ProxyID: proxy.ID, Column2: excluded}); err != nil {
			return fmt.Errorf("could not set excluded inbounds for %s", protocol)
		}
	}
	return nil
}

func resolveProxySettings(raw map[string]json.RawMessage) (map[string]proxysettings.Settings, error) {
	out := map[string]proxysettings.Settings{}
	for protocol, payload := range raw {
		if !proxysettings.ProxyType(protocol).Valid() {
			return nil, fmt.Errorf("unknown proxy type: %s", protocol)
		}
		s, err := proxysettings.FromWire(proxysettings.ProxyType(protocol), payload)
		if err != nil {
			return nil, err
		}
		out[protocol] = s
	}
	return out, nil
}

// handleGetUser implements GET /api/user/:username.
func (h *Handler) handleGetUser(c *gin.Context) {
	dbuser, ok := h.loadAuthorizedUser(c)
	if !ok {
		return
	}
	resp, err := h.buildUserResponse(c.Request.Context(), dbuser)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read user"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// loadAuthorizedUser fetches the :username path param and enforces the same
// ownership rule as the current get_validated_user dependency: sudo sees
// everything, a regular admin only their own users.
func (h *Handler) loadAuthorizedUser(c *gin.Context) (generated.User, bool) {
	identity := auth.CurrentIdentity(c)
	dbuser, err := h.store.Queries.GetUserByUsername(c.Request.Context(), c.Param("username"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "User not found"})
		return generated.User{}, false
	}
	if !identity.IsSudo {
		if !dbuser.AdminID.Valid || dbuser.AdminID.Int32 != identity.AdminID {
			c.JSON(http.StatusForbidden, gin.H{"detail": "You're not allowed"})
			return generated.User{}, false
		}
	}
	return dbuser, true
}

// handleModifyUser implements PUT /api/user/:username, porting
// crud.update_user's proxy/inbound reconciliation and the asymmetric
// status-recompute branches on data_limit/expire changes (see
// D:\MARZBANUPTIMEZE\app\db\crud.py:642-799).
func (h *Handler) handleModifyUser(c *gin.Context) {
	dbuser, ok := h.loadAuthorizedUser(c)
	if !ok {
		return
	}
	var req userWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	if req.Note != nil && len(*req.Note) > 500 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "User's note can be a maximum of 500 character"})
		return
	}
	if req.Status != nil && *req.Status != "" && !validModifyStatus[*req.Status] {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid status"})
		return
	}
	onHoldExpire := normalizeZero(req.OnHoldExpireDuration)
	if req.Status != nil && *req.Status == statusOnHold {
		if onHoldExpire == nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "User cannot be on hold without a valid on_hold_expire_duration."})
			return
		}
		if normalizeZero(req.Expire) != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "User cannot be on hold with specified expire."})
			return
		}
	}
	if req.DataLimitResetStrategy != nil && !validResetStrategy[*req.DataLimitResetStrategy] {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "invalid data_limit_reset_strategy"})
		return
	}

	ctx := c.Request.Context()

	settingsByType, err := resolveProxySettings(req.Proxies)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	for _, tags := range req.Inbounds {
		for _, tag := range tags {
			if _, err := h.store.Queries.GetInboundByTag(ctx, tag); err != nil {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": fmt.Sprintf("Inbound %s doesn't exist", tag)})
				return
			}
		}
	}

	if len(settingsByType) > 0 {
		if err := h.reconcileProxies(ctx, dbuser.ID, settingsByType); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
			return
		}
	}

	if len(req.Inbounds) > 0 {
		if err := h.reconcileExcludedInbounds(ctx, dbuser.ID, req.Inbounds); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
			return
		}
	}

	newStatus := dbuser.Status
	if req.Status != nil && *req.Status != "" {
		newStatus = *req.Status
	}

	newDataLimit := dbuser.DataLimit
	if req.DataLimit != nil {
		dl := normalizeZero(req.DataLimit)
		newDataLimit = int8FromPtr(dl)
		if newStatus != statusExpired && newStatus != statusDisabled {
			if dl == nil || dbuser.UsedTraffic < *dl {
				if newStatus != statusOnHold {
					newStatus = statusActive
				}
			} else {
				newStatus = statusLimited
			}
		}
	}

	newExpire := dbuser.Expire
	if req.Expire != nil {
		ex := normalizeZero(req.Expire)
		newExpire = pgInt4FromPtrInt64(ex)
		if newStatus == statusActive || newStatus == statusExpired {
			if ex == nil || *ex > time.Now().UTC().Unix() {
				newStatus = statusActive
			} else {
				newStatus = statusExpired
			}
		}
	}

	newNote := dbuser.Note
	if req.Note != nil {
		newNote = textFromPtr(normalizeZeroString(req.Note))
	}
	newResetStrategy := dbuser.DataLimitResetStrategy
	if req.DataLimitResetStrategy != nil {
		newResetStrategy = *req.DataLimitResetStrategy
	}
	newOnHoldTimeout := dbuser.OnHoldTimeout
	if req.OnHoldTimeout != nil {
		newOnHoldTimeout = timestamptzFromPtrTime(req.OnHoldTimeout)
	}
	newOnHoldExpireDuration := dbuser.OnHoldExpireDuration
	if req.OnHoldExpireDuration != nil {
		newOnHoldExpireDuration = int8FromPtr(onHoldExpire)
	}
	newAutoDelete := dbuser.AutoDeleteInDays
	if req.AutoDeleteInDays != nil {
		newAutoDelete = pgInt4FromPtr(req.AutoDeleteInDays)
	}

	updated, err := h.store.Queries.UpdateUserCore(ctx, generated.UpdateUserCoreParams{
		ID: dbuser.ID, Status: newStatus, DataLimit: newDataLimit, DataLimitResetStrategy: newResetStrategy,
		Expire: newExpire, Note: newNote, OnHoldExpireDuration: newOnHoldExpireDuration,
		OnHoldTimeout: newOnHoldTimeout, AutoDeleteInDays: newAutoDelete,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not update user"})
		return
	}

	// next_plan: present -> upsert, absent (including explicit null, which
	// is indistinguishable from omitted in the current Python model too) ->
	// delete. Matches crud.update_user's if/elif exactly.
	if req.NextPlan != nil {
		if _, err := h.store.Queries.UpsertNextPlan(ctx, generated.UpsertNextPlanParams{
			UserID: updated.ID, DataLimit: req.NextPlan.DataLimit, Expire: pgInt4FromInt(int(req.NextPlan.Expire)),
			AddRemainingTraffic: req.NextPlan.AddRemainingTraffic, FireOnEither: req.NextPlan.FireOnEither,
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not update next_plan"})
			return
		}
	} else {
		if err := h.store.Queries.DeleteNextPlanByUserID(ctx, updated.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not clear next_plan"})
			return
		}
	}

	resp, err := h.buildUserResponse(ctx, updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "user updated but could not be read back"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// reconcileProxies mirrors crud.update_user's proxy add/update/remove loop:
// a type present in `wanted` gets created (new) or has its settings
// replaced (existing); a type absent from `wanted` gets deleted by its own
// id - never a blanket "delete all proxies for this user", so proxies
// added earlier in the same call are never caught by the removal step.
func (h *Handler) reconcileProxies(ctx context.Context, userID int32, wanted map[string]proxysettings.Settings) error {
	existing, err := h.store.Queries.ListProxiesByUserID(ctx, pgInt4FromInt(int(userID)))
	if err != nil {
		return errors.New("could not read proxies")
	}
	existingByType := map[string]generated.Proxy{}
	for _, p := range existing {
		existingByType[p.Type] = p
	}

	for protocol, settings := range wanted {
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		if p, ok := existingByType[protocol]; ok {
			if _, err := h.store.Queries.UpdateProxySettings(ctx, generated.UpdateProxySettingsParams{ID: p.ID, Settings: raw}); err != nil {
				return fmt.Errorf("could not update %s proxy", protocol)
			}
		} else {
			if _, err := h.store.Queries.CreateProxy(ctx, generated.CreateProxyParams{UserID: pgInt4FromInt(int(userID)), Type: protocol, Settings: raw}); err != nil {
				return fmt.Errorf("could not create %s proxy", protocol)
			}
		}
	}
	for _, p := range existing {
		if _, keep := wanted[p.Type]; !keep {
			if err := h.store.Queries.DeleteProxyByID(ctx, p.ID); err != nil {
				return fmt.Errorf("could not remove %s proxy", p.Type)
			}
		}
	}
	return nil
}

// reconcileExcludedInbounds mirrors crud.update_user's inbounds branch: for
// each protocol present as a key in `wanted` (even with an empty tag list -
// that means "exclude every inbound of this protocol"), find that user's
// existing proxy of the same type and replace its excluded set with
// (known tags for that protocol) - (tags given). A protocol with no
// matching proxy is silently skipped, matching the `if dbproxy:` no-op
// guard in the Python code.
func (h *Handler) reconcileExcludedInbounds(ctx context.Context, userID int32, wanted map[string][]string) error {
	proxies, err := h.store.Queries.ListProxiesByUserID(ctx, pgInt4FromInt(int(userID)))
	if err != nil {
		return errors.New("could not read proxies")
	}
	byType := map[string]int32{}
	for _, p := range proxies {
		byType[p.Type] = p.ID
	}
	for protocol, tags := range wanted {
		proxyID, ok := byType[protocol]
		if !ok {
			continue
		}
		known, err := h.store.Queries.ListInboundTagsByProtocol(ctx, protocol)
		if err != nil {
			return errors.New("could not resolve inbounds")
		}
		excluded := subtractTags(known, tags)
		if err := h.store.Queries.DeleteExcludedInbounds(ctx, proxyID); err != nil {
			return errors.New("could not update excluded inbounds")
		}
		if len(excluded) > 0 {
			if err := h.store.Queries.ReplaceExcludedInbounds(ctx, generated.ReplaceExcludedInboundsParams{ProxyID: proxyID, Column2: excluded}); err != nil {
				return errors.New("could not update excluded inbounds")
			}
		}
	}
	return nil
}

// handleDeleteUser implements DELETE /api/user/:username.
func (h *Handler) handleDeleteUser(c *gin.Context) {
	dbuser, ok := h.loadAuthorizedUser(c)
	if !ok {
		return
	}
	if err := h.store.Queries.DeleteUser(c.Request.Context(), dbuser.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not delete user"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detail": "User removed successfully"})
}

// handleResetUserDataUsage implements POST /api/user/:username/reset,
// matching crud.reset_user_data_usage: log the pre-reset value, zero
// used_traffic, reactivate unless expired/disabled, and cancel any pending
// next_plan.
func (h *Handler) handleResetUserDataUsage(c *gin.Context) {
	dbuser, ok := h.loadAuthorizedUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := h.store.Queries.CreateUserUsageLog(ctx, generated.CreateUserUsageLogParams{
		UserID: pgInt4FromInt(int(dbuser.ID)), UsedTrafficAtReset: dbuser.UsedTraffic,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not log usage reset"})
		return
	}
	newStatus := dbuser.Status
	if newStatus != statusExpired && newStatus != statusDisabled {
		newStatus = statusActive
	}
	updated, err := h.store.Queries.ResetUserTraffic(ctx, generated.ResetUserTrafficParams{ID: dbuser.ID, Status: newStatus})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not reset user data usage"})
		return
	}
	if err := h.store.Queries.DeleteNextPlanByUserID(ctx, updated.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not clear next_plan"})
		return
	}
	resp, err := h.buildUserResponse(ctx, updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read user"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// handleRevokeUserSub implements POST /api/user/:username/revoke_sub:
// rotate every proxy's secret and bump sub_revoked_at. Link/subscription
// URL regeneration is Phase 4 (subscription generation) - not implemented
// here yet.
func (h *Handler) handleRevokeUserSub(c *gin.Context) {
	dbuser, ok := h.loadAuthorizedUser(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	proxies, err := h.store.Queries.ListProxiesByUserID(ctx, pgInt4FromInt(int(dbuser.ID)))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read proxies"})
		return
	}
	for _, p := range proxies {
		settings, err := proxysettings.FromStored(proxysettings.ProxyType(p.Type), p.Settings)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not decode proxy settings"})
			return
		}
		settings.Revoke()
		raw, _ := json.Marshal(settings)
		if _, err := h.store.Queries.UpdateProxySettings(ctx, generated.UpdateProxySettingsParams{ID: p.ID, Settings: raw}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not revoke proxy"})
			return
		}
	}
	updated, err := h.store.Queries.SetUserSubRevoked(ctx, dbuser.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not revoke subscription"})
		return
	}
	resp, err := h.buildUserResponse(ctx, updated)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read user"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// handleListUsers implements GET /api/users: sudo sees every user, a
// regular admin only their own (matching system.py/user.py's `scope =
// dbadmin if not admin.is_sudo else None` pattern).
func (h *Handler) handleListUsers(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	params := generated.ListUsersParams{}
	if !identity.IsSudo {
		params.AdminID = pgInt4FromInt(int(identity.AdminID))
	}
	if v := c.Query("search"); v != "" {
		params.Search = textFromPtr(&v)
	}
	if statuses := c.QueryArray("status"); len(statuses) > 0 {
		params.Statuses = statuses
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			params.Offset = pgInt4FromInt(n)
		}
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			params.Limit = pgInt4FromInt(n)
		}
	}
	rows, err := h.store.Queries.ListUsers(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list users"})
		return
	}
	out := make([]userResponseDTO, 0, len(rows))
	for _, u := range rows {
		resp, err := h.buildUserResponse(c.Request.Context(), u)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read user"})
			return
		}
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, gin.H{"users": out, "total": len(out)})
}

// buildUserResponse assembles the full response, including proxies
// (decoded to the wire shape), inbounds (the inverse of the stored
// exclusions - matches User.inbounds's computed property), and next_plan.
func (h *Handler) buildUserResponse(ctx context.Context, u generated.User) (userResponseDTO, error) {
	proxies, err := h.store.Queries.ListProxiesByUserID(ctx, pgInt4FromInt(int(u.ID)))
	if err != nil {
		return userResponseDTO{}, err
	}
	proxiesOut := map[string]json.RawMessage{}
	inboundsOut := map[string][]string{}
	excludedOut := map[string][]string{}
	for _, p := range proxies {
		proxiesOut[p.Type] = p.Settings
		known, err := h.store.Queries.ListInboundTagsByProtocol(ctx, p.Type)
		if err != nil {
			return userResponseDTO{}, err
		}
		excluded, err := h.store.Queries.ListExcludedInboundTags(ctx, p.ID)
		if err != nil {
			return userResponseDTO{}, err
		}
		excludedOut[p.Type] = excluded
		inboundsOut[p.Type] = subtractTags(known, excluded)
	}

	var adminUsername *string
	if u.AdminID.Valid {
		admin, err := h.store.Queries.GetAdminByID(ctx, u.AdminID.Int32)
		if err == nil {
			adminUsername = &admin.Username
		}
	}

	var nextPlan *nextPlanDTO
	if np, err := h.store.Queries.GetNextPlanByUserID(ctx, u.ID); err == nil {
		nextPlan = &nextPlanDTO{
			DataLimit: np.DataLimit, Expire: int64(np.Expire.Int32),
			AddRemainingTraffic: np.AddRemainingTraffic, FireOnEither: np.FireOnEither,
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return userResponseDTO{}, err
	}

	// lifetime_used_traffic (used_traffic + sum of user_usage_logs) and the
	// per-format `links` array are deferred - see the Phase 2/4 reports.
	lifetimeUsed := u.UsedTraffic

	subToken := subscription.CreateToken(u.Username, h.jwtSecret)
	subURL := h.subURLPrefix + "/sub/" + subToken

	return userResponseDTO{
		ID: u.ID, Username: u.Username, Status: u.Status, UsedTraffic: u.UsedTraffic, LifetimeUsedTraffic: lifetimeUsed,
		DataLimit: int8ToPtr(u.DataLimit), DataLimitResetStrategy: u.DataLimitResetStrategy,
		Expire: pgInt4ToPtrInt64(u.Expire), Note: textToPtr(u.Note), CreatedAt: u.CreatedAt.Time,
		OnHoldExpireDuration: int8ToPtr(u.OnHoldExpireDuration), OnHoldTimeout: timestamptzToPtr(u.OnHoldTimeout),
		AutoDeleteInDays: pgInt4ToPtr(u.AutoDeleteInDays), AdminUsername: adminUsername,
		Proxies: proxiesOut, Inbounds: inboundsOut, ExcludedInbounds: excludedOut, NextPlan: nextPlan,
		SubscriptionURL: subURL,
	}, nil
}

func subtractTags(all, exclude []string) []string {
	excludeSet := make(map[string]bool, len(exclude))
	for _, t := range exclude {
		excludeSet[t] = true
	}
	out := make([]string, 0, len(all))
	for _, t := range all {
		if !excludeSet[t] {
			out = append(out, t)
		}
	}
	return out
}

// normalizeZero mirrors the repeated `x or None` pattern in the current
// Python code: a zero value means "unset", matching NULL, not a real 0.
func normalizeZero(v *int64) *int64 {
	if v == nil || *v == 0 {
		return nil
	}
	return v
}

func normalizeZeroString(v *string) *string {
	if v == nil || *v == "" {
		return nil
	}
	return v
}

func strOr(v *string, fallback string) string {
	if v == nil || *v == "" {
		return fallback
	}
	return *v
}

func pgInt4FromPtrInt64(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func pgInt4ToPtrInt64(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	n := int64(v.Int32)
	return &n
}

func timestamptzFromPtrTime(v *time.Time) pgtype.Timestamptz {
	if v == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *v, Valid: true}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
