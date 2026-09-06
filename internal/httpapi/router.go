package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/auth"
)

type Handler struct {
	store        *Store
	issuer       *auth.TokenIssuer
	sudoUsername string
	sudoPassword string
	jwtSecret    []byte
	publicIP     string
	subURLPrefix string
	logger       *slog.Logger
}

func NewHandler(store *Store, issuer *auth.TokenIssuer, sudoUsername, sudoPassword string, jwtSecret []byte, publicIP, subURLPrefix string, logger *slog.Logger) *Handler {
	return &Handler{
		store: store, issuer: issuer, sudoUsername: sudoUsername, sudoPassword: sudoPassword,
		jwtSecret: jwtSecret, publicIP: publicIP, subURLPrefix: subURLPrefix, logger: logger,
	}
}

// NewRouter builds the Gin engine with logging/recovery middleware and every
// route this phase implements, under the /api prefix the current FastAPI
// app also uses.
func NewRouter(h *Handler, logger *slog.Logger, allowedOrigins []string) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(slogMiddleware(logger), gin.Recovery(), CORS(allowedOrigins))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	requireAdmin := auth.RequireAdmin(h.issuer, h.store, h.sudoUsername)
	requireSudo := auth.RequireSudo(h.issuer, h.store, h.sudoUsername)

	api := r.Group("/api")
	{
		api.POST("/admin/token", h.handleLogin)
		api.GET("/admin", requireAdmin, h.handleGetCurrentAdmin)

		// Unlike FastAPI, Gin's router always prefers a static segment over
		// a :param one at the same level, so these don't need to precede
		// /admin/:username below to avoid being shadowed by it - registered
		// together here purely for readability.
		api.GET("/admin/inactive", requireSudo, h.handleGetInactiveAdmins)
		api.DELETE("/admin/inactive", requireSudo, h.handleDeleteInactiveAdmins)

		api.POST("/admin", requireSudo, h.handleCreateAdmin)
		api.GET("/admins", requireSudo, h.handleListAdmins)
		api.PUT("/admin/:username", requireSudo, h.handleUpdateAdmin)
		api.DELETE("/admin/:username", requireSudo, h.handleDeleteAdmin)

		api.GET("/inbounds", requireAdmin, h.handleListInbounds)
		api.POST("/inbounds/sync", requireSudo, h.handleSyncInbounds)

		api.GET("/hosts", requireSudo, h.handleGetHosts)
		api.PUT("/hosts", requireSudo, h.handlePutHosts)

		api.POST("/user", requireAdmin, h.handleCreateUser)
		api.GET("/users", requireAdmin, h.handleListUsers)
		api.GET("/user/:username", requireAdmin, h.handleGetUser)
		api.PUT("/user/:username", requireAdmin, h.handleModifyUser)
		api.DELETE("/user/:username", requireAdmin, h.handleDeleteUser)
		api.POST("/user/:username/reset", requireAdmin, h.handleResetUserDataUsage)
		api.POST("/user/:username/revoke_sub", requireAdmin, h.handleRevokeUserSub)

		api.POST("/user_template", requireSudo, h.handleCreateUserTemplate)
		api.GET("/user_template", requireAdmin, h.handleListUserTemplates)
		api.GET("/user_template/:id", requireAdmin, h.handleGetUserTemplate)
		api.PUT("/user_template/:id", requireSudo, h.handleModifyUserTemplate)
		api.DELETE("/user_template/:id", requireSudo, h.handleDeleteUserTemplate)

		api.POST("/node", requireSudo, h.handleCreateNode)
		api.GET("/nodes", requireSudo, h.handleListNodes)
		api.GET("/node/:id", requireSudo, h.handleGetNode)
		api.DELETE("/node/:id", requireSudo, h.handleDeleteNode)
	}

	r.GET("/sub/:token", h.handleGetSubscription)
	r.GET("/sub/:token/:format", h.handleGetSubscriptionFormat)

	return r
}

func slogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}
