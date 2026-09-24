package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

type adminDTO struct {
	// Nullable: the env-bootstrapped sudo account has no admins row at
	// all, and the real panel reports its id as null there. Emitting 0
	// instead let a client both mistake it for a real admin id and fail
	// the "is this the bootstrap account?" null check.
	ID             *int32  `json:"id"`
	Username       string  `json:"username"`
	IsSudo         bool    `json:"is_sudo"`
	IsOwner        bool    `json:"is_owner"`
	TelegramID     *int64  `json:"telegram_id"`
	DiscordWebhook *string `json:"discord_webhook"`
	UsersUsage     *int64  `json:"users_usage"`
}

func toAdminDTO(a generated.Admin) adminDTO {
	usage := a.UsersUsage
	id := a.ID
	return adminDTO{
		ID:             &id,
		Username:       a.Username,
		IsSudo:         a.IsSudo,
		IsOwner:        a.IsOwner,
		TelegramID:     int8ToPtr(a.TelegramID),
		DiscordWebhook: textToPtr(a.DiscordWebhook),
		UsersUsage:     &usage,
	}
}

type adminCreateRequest struct {
	Username       string  `json:"username" binding:"required"`
	Password       string  `json:"password" binding:"required"`
	IsSudo         bool    `json:"is_sudo"`
	TelegramID     *int64  `json:"telegram_id"`
	DiscordWebhook *string `json:"discord_webhook"`
}

// IsSudo/IsOwner are *bool (not bool) specifically so "omitted" and
// "explicitly false" are distinguishable - see handleUpdateAdmin's own
// comment on why that distinction is the whole point: revoking either flag
// is owner-only, but leaving a field out of the request must never revoke
// it by accident just because Go's bool zero value is false.
type adminModifyRequest struct {
	Password       *string `json:"password"`
	IsSudo         *bool   `json:"is_sudo"`
	IsOwner        *bool   `json:"is_owner"`
	TelegramID     *int64  `json:"telegram_id"`
	DiscordWebhook *string `json:"discord_webhook"`
}

func validateDiscordWebhook(v *string) error {
	if v != nil && *v != "" && !strings.HasPrefix(*v, "https://discord.com") {
		return errors.New("Discord webhook must start with 'https://discord.com'")
	}
	return nil
}

// loginRateLimitWindow/Max bound POST /api/admin/token's *failed* attempts
// per source IP - neither this panel nor the real Python original has ever
// had any login rate limit at all (a real, documented gap, not a
// deliberate scope cut). This counts failures only, never successes: a
// live compatibility test against a real, unmodified external reseller bot
// showed it re-authenticates fresh on nearly every single API
// call rather than caching its token - a legitimate bot doing a burst of
// real operations can rack up far more than 10 *correct* logins a minute,
// and counting those would have made this fix break exactly the
// compatibility it was meant to protect. 20 wrong-password attempts/minute
// is still a meaningful cap on wasted bcrypt CPU and brute-force/enumeration
// risk, without touching anyone whose credentials are simply correct.
const (
	loginRateLimitWindow = time.Minute
	loginRateLimitMax    = 20
)

// tooManyFailedLogins peeks the per-IP failure counter without bumping it -
// call recordFailedLogin separately, only once a login has actually been
// confirmed wrong. Fails OPEN (never blocks) if Redis itself is unreachable
// or the key doesn't exist yet - availability of the login path matters
// more than this specific defense-in-depth layer.
func (h *Handler) tooManyFailedLogins(ctx context.Context, ip string) bool {
	val, err := h.store.Cache.Get(ctx, cache.LoginAttemptsKey(ip))
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return false
	}
	return n >= loginRateLimitMax
}

func (h *Handler) recordFailedLogin(ctx context.Context, ip string) {
	if _, err := h.store.Cache.Incr(ctx, cache.LoginAttemptsKey(ip), loginRateLimitWindow); err != nil {
		h.logger.Warn("record failed login", "error", err)
	}
}

// clearFailedLogins runs on every successful login so a real admin who
// mistypes a password a few times before getting it right isn't left
// sitting close to the limit for the rest of the window.
func (h *Handler) clearFailedLogins(ctx context.Context, ip string) {
	if err := h.store.Cache.Del(ctx, cache.LoginAttemptsKey(ip)); err != nil {
		h.logger.Warn("clear failed logins", "error", err)
	}
}

// handleLogin implements POST /api/admin/token, matching the current
// OAuth2PasswordRequestForm contract (form-encoded username/password).
// The env-bootstrapped sudo account is checked first, in plaintext, with no
// DB row required at all - exactly like SUDOERS.get(username) == password
// in app/dependencies.py's validate_admin.
func (h *Handler) handleLogin(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")
	ip := clientIP(c)
	ctx := c.Request.Context()
	c.Set(contextLoginUsernameKey, username)

	if h.tooManyFailedLogins(ctx, ip) {
		c.Set(contextLoginErrorKey, "rate-limited")
		c.JSON(http.StatusTooManyRequests, gin.H{"detail": "Too many login attempts, please try again later"})
		return
	}

	var isSudo bool
	switch {
	case h.sudoUsername != "" && username == h.sudoUsername && password == h.sudoPassword:
		isSudo = true
	default:
		admin, err := h.store.Queries.GetAdminByUsername(ctx, username)
		if err != nil {
			// Logged at warn with the attempted username and source IP -
			// never the password. Without this, a reseller whose bot is
			// stuck in a login-retry loop is indistinguishable in the logs
			// from any other 401, which cost a lot of time to diagnose by
			// hand; with it, "which account is actually failing" is one
			// grep away.
			c.Set(contextLoginErrorKey, "no such admin")
			h.recordFailedLogin(ctx, ip)
			h.reports.Login(ctx, username, ip, loginStatusFailed)
			c.JSON(http.StatusUnauthorized, gin.H{"detail": "Incorrect username or password"})
			return
		}
		if !auth.VerifyPassword(password, admin.HashedPassword) {
			c.Set(contextLoginErrorKey, "wrong password")
			h.recordFailedLogin(ctx, ip)
			h.reports.Login(ctx, username, ip, loginStatusFailed)
			c.JSON(http.StatusUnauthorized, gin.H{"detail": "Incorrect username or password"})
			return
		}
		isSudo = admin.IsSudo
	}

	token, err := h.issuer.Issue(username, isSudo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not issue token"})
		return
	}
	h.clearFailedLogins(ctx, ip)
	if !contains(h.loginNotifyWhitelist, ip) {
		h.reports.Login(ctx, username, ip, loginStatusSuccess)
	}
	c.JSON(http.StatusOK, gin.H{"access_token": token, "token_type": "bearer"})
}

// loginStatusSuccess/Failed mirror app/utils/report.py's login() wrapper:
// "✅ Success"/"❌ Failed" text, not a bare boolean, since that's what
// ends up rendered in the notification.
const (
	loginStatusSuccess = "✅ Success"
	loginStatusFailed  = "❌ Failed"
)

// contextLoginUsernameKey / contextLoginErrorKey hand the attempted
// username and the failure reason to the API-client log
// (internal/httpapi/apiclientlog.go) - a login request has no Identity to
// read them off, and the username lives in the form body, which that
// middleware deliberately never touches.
const (
	contextLoginUsernameKey = "login.username"
	contextLoginErrorKey    = "login.error"
)

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// handleGetCurrentAdmin implements GET /api/admin.
func (h *Handler) handleGetCurrentAdmin(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	if h.sudoUsername != "" && identity.Username == h.sudoUsername && identity.IsSudo {
		c.JSON(http.StatusOK, adminDTO{Username: identity.Username, IsSudo: true, IsOwner: true})
		return
	}
	admin, err := h.store.CachedGetAdminByUsername(c.Request.Context(), identity.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "Could not validate credentials"})
		return
	}
	c.JSON(http.StatusOK, toAdminDTO(admin))
}

// handleCreateAdmin implements POST /api/admin (sudo only).
func (h *Handler) handleCreateAdmin(c *gin.Context) {
	var req adminCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	if err := validateDiscordWebhook(req.DiscordWebhook); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	hashed, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not hash password"})
		return
	}
	admin, err := h.store.Queries.CreateAdmin(c.Request.Context(), generated.CreateAdminParams{
		Username:       req.Username,
		HashedPassword: hashed,
		IsSudo:         req.IsSudo,
		TelegramID:     int8FromPtr(req.TelegramID),
		DiscordWebhook: textFromPtr(req.DiscordWebhook),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			c.JSON(http.StatusConflict, gin.H{"detail": "Admin already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not create admin"})
		return
	}
	c.JSON(http.StatusOK, toAdminDTO(admin))
}

// handleListAdmins implements GET /api/admins (sudo only).
func (h *Handler) handleListAdmins(c *gin.Context) {
	params := generated.ListAdminsParams{}
	if v := c.Query("username"); v != "" {
		params.Username = textFromPtr(&v)
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			params.Offset = pgInt4FromInt(n)
		}
	}
	if v := c.Query("limit"); v != "" {
		// limit=0 means "no limit" on the real panel, not "no rows" - a
		// client using it to fetch everything got an empty list here and
		// concluded the panel had no admins at all.
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			params.Limit = pgInt4FromInt(n)
		}
	}
	admins, err := h.store.Queries.ListAdmins(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not list admins"})
		return
	}
	out := make([]adminDTO, 0, len(admins))
	for _, a := range admins {
		out = append(out, toAdminDTO(a))
	}
	c.JSON(http.StatusOK, out)
}

// handleUpdateAdmin implements PUT /api/admin/{username} (sudo only, with
// extra owner-only powers layered on top - see the guards below).
func (h *Handler) handleUpdateAdmin(c *gin.Context) {
	username := c.Param("username")
	current := auth.CurrentIdentity(c)

	admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Admin not found"})
		return
	}
	// An owner is the one caller allowed past this: everyone else, sudo or
	// not, is still refused outright from touching another sudo account at
	// all - matching the existing password/telegram/discord protection
	// sudo accounts have always had from each other.
	if admin.Username != current.Username && admin.IsSudo && !current.IsOwner {
		c.JSON(http.StatusForbidden, gin.H{"detail": "You're not allowed to edit another sudoer's account. Use rapido-cli instead."})
		return
	}

	var req adminModifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}
	if err := validateDiscordWebhook(req.DiscordWebhook); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": err.Error()})
		return
	}

	params := generated.UpdateAdminParams{
		ID:              admin.ID,
		IsSudo:          admin.IsSudo,
		HashedPassword:  admin.HashedPassword,
		PasswordResetAt: admin.PasswordResetAt,
		TelegramID:      admin.TelegramID,
		DiscordWebhook:  admin.DiscordWebhook,
		IsOwner:         admin.IsOwner,
	}
	// req.IsSudo/IsOwner are *bool precisely so "omitted" (nil, leave
	// unchanged) is distinguishable from "explicitly false" (revoke) - but
	// only an ACTUAL change triggers a permission check at all. The
	// AdminForm always sends is_sudo (its checkbox has no third "don't
	// touch" state), so a non-owner admin saving someone who is already
	// non-sudo must stay a harmless no-op, not a spurious 403 just because
	// the wire value happens to be `false`.
	//
	// Granting either flag is allowed for anyone who reached this far
	// (this route already requires sudo, and only an owner gets past the
	// guard above for someone else's sudo account), but REVOKING either is
	// owner-only, full stop, even on your own account: sudo status used to
	// be one-way (crud.update_admin's old "only overwrite if truthy"
	// semantics meant nobody, ever, could turn it back off through this
	// endpoint) - the ask this replaces was specifically for a way to
	// reverse that, gated behind a real permission check rather than left
	// impossible for everyone.
	if req.IsSudo != nil && *req.IsSudo != admin.IsSudo {
		if *req.IsSudo {
			params.IsSudo = true
		} else if current.IsOwner {
			params.IsSudo = false
		} else {
			c.JSON(http.StatusForbidden, gin.H{"detail": "Only an owner can revoke sudo access"})
			return
		}
	}
	if req.IsOwner != nil && *req.IsOwner != admin.IsOwner {
		if !current.IsOwner {
			c.JSON(http.StatusForbidden, gin.H{"detail": "Only an owner can grant or revoke owner access"})
			return
		}
		if admin.Username == current.Username && !*req.IsOwner {
			c.JSON(http.StatusForbidden, gin.H{"detail": "An owner cannot remove their own owner access - have another owner do it"})
			return
		}
		params.IsOwner = *req.IsOwner
	}
	if req.Password != nil && *req.Password != "" {
		hashed, err := auth.HashPassword(*req.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not hash password"})
			return
		}
		if hashed != admin.HashedPassword {
			params.HashedPassword = hashed
			params.PasswordResetAt = timestamptzFromTime(time.Now().UTC())
		}
	}
	if req.TelegramID != nil && *req.TelegramID != 0 {
		params.TelegramID = int8FromPtr(req.TelegramID)
	}
	if req.DiscordWebhook != nil && *req.DiscordWebhook != "" {
		params.DiscordWebhook = textFromPtr(req.DiscordWebhook)
	}

	updated, err := h.store.Queries.UpdateAdmin(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not update admin"})
		return
	}
	if err := h.store.InvalidateAdmin(c.Request.Context(), updated.ID, updated.Username); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not invalidate admin cache"})
		return
	}
	c.JSON(http.StatusOK, toAdminDTO(updated))
}

// handleDeleteAdmin implements DELETE /api/admin/{username} (sudo only).
func (h *Handler) handleDeleteAdmin(c *gin.Context) {
	username := c.Param("username")
	admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Admin not found"})
		return
	}
	if admin.IsSudo {
		c.JSON(http.StatusForbidden, gin.H{"detail": "You're not allowed to delete sudo accounts. Use rapido-cli instead."})
		return
	}
	if err := h.store.Queries.DeleteAdmin(c.Request.Context(), admin.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not delete admin"})
		return
	}
	if err := h.store.InvalidateAdmin(c.Request.Context(), admin.ID, admin.Username); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not invalidate admin cache"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Admin removed successfully"})
}

// handleDisableAdminUsers implements POST /api/admin/{username}/users/disable
// (sudo only) - a bulk moderation action, e.g. for a reseller who stopped
// paying: every active or on_hold user under this admin goes to disabled in
// one call, without deleting them.
func (h *Handler) handleDisableAdminUsers(c *gin.Context) {
	username := c.Param("username")
	admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Admin not found"})
		return
	}
	users, err := h.store.Queries.DisableActiveUsersByAdminID(c.Request.Context(), pgInt4FromInt(int(admin.ID)))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not disable this admin's users"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Users successfully disabled", "users_affected": len(users)})
}

// handleActivateAdminUsers implements POST /api/admin/{username}/users/activate
// (sudo only) - the reverse of handleDisableAdminUsers.
func (h *Handler) handleActivateAdminUsers(c *gin.Context) {
	username := c.Param("username")
	admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Admin not found"})
		return
	}
	users, err := h.store.Queries.ActivateDisabledUsersByAdminID(c.Request.Context(), pgInt4FromInt(int(admin.ID)))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not activate this admin's users"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Users successfully activated", "users_affected": len(users)})
}

// handleResetAdminUsage implements POST /api/admin/usage/reset/{username}
// (sudo only) - zeroes the "Traffic sold" counter shown on the admin's own
// card, archiving the prior value into admin_usage_logs first (see
// ResetAdminUsage's own doc comment).
func (h *Handler) handleResetAdminUsage(c *gin.Context) {
	username := c.Param("username")
	admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Admin not found"})
		return
	}
	updated, err := h.store.Queries.ResetAdminUsage(c.Request.Context(), admin.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not reset admin usage"})
		return
	}
	if err := h.store.InvalidateAdmin(c.Request.Context(), updated.ID, updated.Username); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not invalidate admin cache"})
		return
	}
	c.JSON(http.StatusOK, toAdminDTO(updated))
}

type inactiveAdminDTO struct {
	Username     string    `json:"username"`
	LastActivity time.Time `json:"last_activity"`
	UserCount    int64     `json:"user_count"`
}

// handleGetInactiveAdmins implements GET /api/admin/inactive (sudo only).
// Registered ahead of PUT/DELETE /admin/{username} in the router - those
// would otherwise match "inactive" as a username.
func (h *Handler) handleGetInactiveAdmins(c *gin.Context) {
	days := 90
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			days = n
		}
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	rows, err := h.store.Queries.GetInactiveAdmins(c.Request.Context(), timestamptzFromTime(cutoff))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not fetch inactive admins"})
		return
	}
	admins := make([]inactiveAdminDTO, 0, len(rows))
	for _, r := range rows {
		admins = append(admins, inactiveAdminDTO{
			Username:     r.Username,
			LastActivity: r.LastActivity.Time,
			UserCount:    r.UserCount,
		})
	}
	c.JSON(http.StatusOK, gin.H{"cutoff_days": days, "admins": admins})
}

// handleDeleteInactiveAdmins implements DELETE /api/admin/inactive (sudo
// only). Deletes each inactive admin's users and the admin itself at the DB
// level; dispatching node-side teardown for each removed user is deferred
// to the node/RPC layer built in a later phase (see DeleteUsersByAdminID).
func (h *Handler) handleDeleteInactiveAdmins(c *gin.Context) {
	days := 90
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			days = n
		}
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	rows, err := h.store.Queries.GetInactiveAdmins(c.Request.Context(), timestamptzFromTime(cutoff))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not fetch inactive admins"})
		return
	}

	removed := make([]inactiveAdminDTO, 0, len(rows))
	usersRemoved := 0
	for _, r := range rows {
		users, err := h.store.Queries.DeleteUsersByAdminID(c.Request.Context(), pgInt4FromInt(int(r.ID)))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not remove users for inactive admin"})
			return
		}
		if err := h.store.Queries.DeleteAdmin(c.Request.Context(), r.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not remove inactive admin"})
			return
		}
		if err := h.store.InvalidateAdmin(c.Request.Context(), r.ID, r.Username); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not invalidate admin cache"})
			return
		}
		usersRemoved += len(users)
		removed = append(removed, inactiveAdminDTO{
			Username:     r.Username,
			LastActivity: r.LastActivity.Time,
			UserCount:    r.UserCount,
		})
	}

	c.JSON(http.StatusOK, gin.H{"cutoff_days": days, "admins": removed, "users_removed": usersRemoved})
}
