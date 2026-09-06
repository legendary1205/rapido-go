package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

type adminDTO struct {
	ID             int32   `json:"id"`
	Username       string  `json:"username"`
	IsSudo         bool    `json:"is_sudo"`
	TelegramID     *int64  `json:"telegram_id"`
	DiscordWebhook *string `json:"discord_webhook"`
	UsersUsage     *int64  `json:"users_usage"`
}

func toAdminDTO(a generated.Admin) adminDTO {
	usage := a.UsersUsage
	return adminDTO{
		ID:             a.ID,
		Username:       a.Username,
		IsSudo:         a.IsSudo,
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

type adminModifyRequest struct {
	Password       *string `json:"password"`
	IsSudo         bool    `json:"is_sudo"`
	TelegramID     *int64  `json:"telegram_id"`
	DiscordWebhook *string `json:"discord_webhook"`
}

func validateDiscordWebhook(v *string) error {
	if v != nil && *v != "" && !strings.HasPrefix(*v, "https://discord.com") {
		return errors.New("Discord webhook must start with 'https://discord.com'")
	}
	return nil
}

// handleLogin implements POST /api/admin/token, matching the current
// OAuth2PasswordRequestForm contract (form-encoded username/password).
// The env-bootstrapped sudo account is checked first, in plaintext, with no
// DB row required at all - exactly like SUDOERS.get(username) == password
// in app/dependencies.py's validate_admin.
func (h *Handler) handleLogin(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")

	var isSudo bool
	switch {
	case h.sudoUsername != "" && username == h.sudoUsername && password == h.sudoPassword:
		isSudo = true
	default:
		admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), username)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"detail": "Incorrect username or password"})
			return
		}
		if !auth.VerifyPassword(password, admin.HashedPassword) {
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
	c.JSON(http.StatusOK, gin.H{"access_token": token, "token_type": "bearer"})
}

// handleGetCurrentAdmin implements GET /api/admin.
func (h *Handler) handleGetCurrentAdmin(c *gin.Context) {
	identity := auth.CurrentIdentity(c)
	if h.sudoUsername != "" && identity.Username == h.sudoUsername && identity.IsSudo {
		c.JSON(http.StatusOK, adminDTO{Username: identity.Username, IsSudo: true})
		return
	}
	admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), identity.Username)
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
		if n, err := strconv.Atoi(v); err == nil {
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

// handleUpdateAdmin implements PUT /api/admin/{username} (sudo only).
func (h *Handler) handleUpdateAdmin(c *gin.Context) {
	username := c.Param("username")
	current := auth.CurrentIdentity(c)

	admin, err := h.store.Queries.GetAdminByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Admin not found"})
		return
	}
	if admin.Username != current.Username && admin.IsSudo {
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
	}
	// Mirrors crud.update_admin's exact (and slightly quirky) semantics:
	// each field only overwrites if truthy, so is_sudo can be turned on but
	// never back off through this endpoint, and an empty telegram_id/webhook
	// leaves the existing value untouched.
	if req.IsSudo {
		params.IsSudo = true
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
	c.JSON(http.StatusOK, gin.H{"detail": "Admin removed successfully"})
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
		usersRemoved += len(users)
		removed = append(removed, inactiveAdminDTO{
			Username:     r.Username,
			LastActivity: r.LastActivity.Time,
			UserCount:    r.UserCount,
		})
	}

	c.JSON(http.StatusOK, gin.H{"cutoff_days": days, "admins": removed, "users_removed": usersRemoved})
}
