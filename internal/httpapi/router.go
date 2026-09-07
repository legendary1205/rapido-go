package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/hostmetrics"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/kirbot"
	"github.com/legendary1205/rapido-go/internal/report"
)

type Handler struct {
	store                *Store
	issuer               *auth.TokenIssuer
	sudoUsername         string
	sudoPassword         string
	jwtSecret            []byte
	publicIP             string
	subURLPrefix         string
	envDefaults          integrationsettings.Values
	reports              *report.Dispatcher
	kirbot               *kirbot.Client
	loginNotifyWhitelist []string
	hostMetricsTracker   *hostmetrics.PreviousTracker
	logger               *slog.Logger

	databaseURL     string
	backupDir       string
	backupKeep      int
	dumpDatabase    dumpDatabaseFn
	restoreDatabase restoreDatabaseFn
}

func NewHandler(store *Store, issuer *auth.TokenIssuer, sudoUsername, sudoPassword string, jwtSecret []byte,
	publicIP, subURLPrefix string, envDefaults integrationsettings.Values, reports *report.Dispatcher,
	kirbotClient *kirbot.Client, loginNotifyWhitelist []string, hostMetricsTracker *hostmetrics.PreviousTracker,
	databaseURL, backupDir string, backupKeep int,
	logger *slog.Logger) *Handler {
	return &Handler{
		store: store, issuer: issuer, sudoUsername: sudoUsername, sudoPassword: sudoPassword,
		jwtSecret: jwtSecret, publicIP: publicIP, subURLPrefix: subURLPrefix,
		envDefaults: envDefaults, reports: reports, kirbot: kirbotClient,
		loginNotifyWhitelist: loginNotifyWhitelist, hostMetricsTracker: hostMetricsTracker,
		databaseURL: databaseURL, backupDir: backupDir, backupKeep: backupKeep,
		dumpDatabase:    func(ctx context.Context, w *os.File) error { return execPgDump(ctx, databaseURL, w) },
		restoreDatabase: func(ctx context.Context, gz io.Reader) error { return execPsqlRestore(ctx, databaseURL, gz) },
		logger:          logger,
	}
}

// NewRouter builds the Gin engine with logging/recovery middleware and every
// route this phase implements, under the /api prefix the current FastAPI
// app also uses.
func NewRouter(h *Handler, logger *slog.Logger, allowedOrigins []string) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(slogMiddleware(logger), gin.Recovery(), CORS(allowedOrigins), maintenanceMiddleware(h.store))

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

		// Read-only aggregate endpoints for the dashboard's Overview page -
		// available to every admin (scoped to their own users when not sudo),
		// not sudo-gated.
		api.GET("/system", requireAdmin, h.handleGetSystemStats)
		api.GET("/system/usage-history", requireAdmin, h.handleGetSystemUsageHistory)

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
		api.GET("/nodes/usage", requireSudo, h.handleGetNodesUsage)
		api.GET("/node/:id", requireSudo, h.handleGetNode)
		api.PUT("/node/:id", requireSudo, h.handleUpdateNode)
		api.DELETE("/node/:id", requireSudo, h.handleDeleteNode)

		api.GET("/monitoring", requireSudo, h.handleGetMonitoring)
		api.GET("/monitoring/history", requireSudo, h.handleGetMonitoringHistory)

		api.GET("/settings/integrations", requireSudo, h.handleGetIntegrationSettings)
		api.PUT("/settings/integrations", requireSudo, h.handleUpdateIntegrationSettings)

		api.GET("/settings/core-config", requireSudo, h.handleGetCoreConfig)
		api.PUT("/settings/core-config", requireSudo, h.handleUpdateCoreConfig)

		api.GET("/settings/backup", requireSudo, h.handleListBackups)
		api.POST("/settings/backup", requireSudo, h.handleCreateBackup)
		api.GET("/settings/backup/:filename", requireSudo, h.handleDownloadBackup)
		api.DELETE("/settings/backup/:filename", requireSudo, h.handleDeleteBackup)
		api.POST("/settings/backup/:filename/restore", requireSudo, h.handleRestoreBackup)
		api.POST("/settings/backup/restore-upload", requireSudo, h.handleRestoreUpload)

		api.GET("/tickets", requireAdmin, h.handleListTickets)
		api.GET("/tickets/:id", requireAdmin, h.handleGetTicket)
		api.POST("/tickets/:id/messages", requireAdmin, h.handleAdminReplyTicket)
		api.PUT("/tickets/:id", requireAdmin, h.handleUpdateTicketStatus)

		// Node -> panel, not admin -> panel: authenticated by a per-node
		// bearer secret (requireNodeSecret), never an admin JWT. Must be
		// pointed at the backend-singleton's own address in deployment, not
		// a load-balanced API pool - see handleNodeReport's doc comment.
		api.POST("/internal/node-report", h.requireNodeSecret, h.handleNodeReport)
		// Panel -> node config pull (see handleGetNodeConfig's doc comment) -
		// every node polls this on the same interval as node-report above.
		api.GET("/internal/node-config", h.requireNodeSecret, h.handleGetNodeConfig)
	}

	r.GET("/sub/:token", h.handleGetSubscription)
	r.GET("/sub/:token/:format", h.handleGetSubscriptionFormat)
	r.GET("/sub/:token/info", h.handleSubscriptionInfo)
	r.POST("/sub/:token/emergency", h.handleEmergencyRecharge)
	r.GET("/sub/:token/tickets", h.handleListMyTickets)
	r.POST("/sub/:token/tickets", h.handleCreateMyTicket)
	r.POST("/sub/:token/tickets/:id/messages", h.handleReplyMyTicket)

	return r
}

// maintenanceMiddleware rejects every request except /health while a
// database restore (internal/httpapi/backup.go's restoreFromReader) is in
// progress - the schema itself may not exist for a moment during a
// restore's drop+recreate, so letting requests through would just trade a
// clean 503 for a confusing 500 mid-query. /health stays reachable so an
// external monitor doesn't flap the whole process as down over an
// expected, bounded restore window.
func maintenanceMiddleware(store *Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/health" {
			c.Next()
			return
		}
		if on, err := store.Cache.IsMaintenanceMode(c.Request.Context()); err == nil && on {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"detail": "The panel is restoring a database backup - try again shortly"})
			return
		}
		c.Next()
	}
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
