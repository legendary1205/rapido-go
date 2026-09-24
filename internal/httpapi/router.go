package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/hostmetrics"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/resellerapi"
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
	subURLPrefixes       []string
	clashTemplatePath    string
	v2rayTemplatePath    string
	formatFlags          SubscriptionFormatFlags
	subBranding          SubscriptionBranding
	envDefaults          integrationsettings.Values
	reports              *report.Dispatcher
	resellerapi               *resellerapi.Client
	loginNotifyWhitelist []string
	hostMetricsTracker   *hostmetrics.PreviousTracker
	logger               *slog.Logger

	// Memoized direct host read for GET /api/system - see cachedHostSample.
	hostSampleMu    sync.Mutex
	hostSampleAt    time.Time
	hostSampleTotal int64
	hostSampleUsed  int64
	hostSampleCores int

	databaseURL     string
	backupDir       string
	backupKeep      int
	dumpDatabase    dumpDatabaseFn
	restoreDatabase restoreDatabaseFn
}

// SubscriptionBranding carries the three operator-facing values the real
// panel exposes on every subscription response (SUB_SUPPORT_URL,
// SUB_PROFILE_TITLE, SUB_UPDATE_INTERVAL). Real VPN clients render the
// first two as buttons, so leaving them empty is visible to customers.
type SubscriptionBranding struct {
	SupportURL     string
	ProfileTitle   string
	UpdateInterval string
}

func (b SubscriptionBranding) withDefaults() SubscriptionBranding {
	if b.SupportURL == "" {
		b.SupportURL = "https://t.me/"
	}
	if b.ProfileTitle == "" {
		b.ProfileTitle = "Subscription"
	}
	if b.UpdateInterval == "" {
		b.UpdateInterval = "12"
	}
	return b
}

func NewHandler(store *Store, issuer *auth.TokenIssuer, sudoUsername, sudoPassword string, jwtSecret []byte,
	publicIP, subURLPrefix, clashTemplatePath, v2rayTemplatePath string, formatFlags SubscriptionFormatFlags,
	subBranding SubscriptionBranding,
	envDefaults integrationsettings.Values, reports *report.Dispatcher,
	resellerAPIClient *resellerapi.Client, loginNotifyWhitelist []string, hostMetricsTracker *hostmetrics.PreviousTracker,
	databaseURL, backupDir string, backupKeep int,
	logger *slog.Logger) *Handler {
	return &Handler{
		store: store, issuer: issuer, sudoUsername: sudoUsername, sudoPassword: sudoPassword,
		jwtSecret: jwtSecret, publicIP: publicIP, subURLPrefix: subURLPrefix,
		clashTemplatePath: clashTemplatePath, v2rayTemplatePath: v2rayTemplatePath, formatFlags: formatFlags,
		subBranding: subBranding.withDefaults(),
		envDefaults: envDefaults, reports: reports, resellerapi: resellerAPIClient,
		loginNotifyWhitelist: loginNotifyWhitelist, hostMetricsTracker: hostMetricsTracker,
		databaseURL: databaseURL, backupDir: backupDir, backupKeep: backupKeep,
		dumpDatabase:    func(ctx context.Context, w *os.File) error { return execPgDump(ctx, databaseURL, w) },
		restoreDatabase: func(ctx context.Context, gz io.Reader) error { return execPsqlRestore(ctx, databaseURL, gz) },
		logger:          logger,
	}
}

// WithSubscriptionURLPrefixes sets every address a subscription is reachable
// on, in dashboard display order - see config.SubscriptionURLPrefixes. The
// first one is also the single address handed to everything that takes just
// one link (`subscription_url`, which reseller bots read, and the
// subscription page), so it replaces subURLPrefix; an empty list leaves
// subURLPrefix in effect. A setter rather than another NewHandler
// parameter: that list is already long.
func (h *Handler) WithSubscriptionURLPrefixes(prefixes []string) *Handler {
	h.subURLPrefixes = prefixes
	if len(prefixes) > 0 {
		h.subURLPrefix = strings.TrimRight(prefixes[0], "/")
	}
	return h
}

// subscriptionURLs is the list of links for one subscription token: every
// configured prefix in order, or just the single legacy link when none is set.
func (h *Handler) subscriptionURLs(token string) []string {
	if len(h.subURLPrefixes) == 0 {
		return []string{h.subURLPrefix + "/sub/" + token}
	}
	urls := make([]string, 0, len(h.subURLPrefixes))
	for _, prefix := range h.subURLPrefixes {
		urls = append(urls, strings.TrimRight(prefix, "/")+"/sub/"+token)
	}
	return urls
}

// NewRouter builds the Gin engine with logging/recovery middleware and every
// route this phase implements, under the /api prefix the current FastAPI
// app also uses.
func NewRouter(h *Handler, logger *slog.Logger, allowedOrigins []string) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(slogMiddleware(logger), apiClientLogMiddleware(logger), gin.Recovery(), CORS(allowedOrigins), maintenanceMiddleware(h.store))

	// Gin's own defaults answer an unknown path with the plain-text body
	// "404 page not found" and a wrong method with that same 404. FastAPI -
	// which every existing external API client was written against - always
	// answers JSON, and distinguishes the two. That difference is not
	// cosmetic: a bot that json_decode()s the body gets null from plain
	// text and reports the whole panel as unreachable rather than "that one
	// call 404'd", which is exactly the "server unavailable" symptom real
	// reseller bots hit here.
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Not Found"})
	})
	r.HandleMethodNotAllowed = true
	r.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"detail": "Method Not Allowed"})
	})

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// The bare root is a 200 with an empty document - what an admin sees on
	// the real panel when they open the domain without /dashboard, and a
	// reachability probe for any client that checks the root. The dashboard
	// itself lives only under /dashboard/ (see MountDashboardStatic): its
	// asset paths are built for that prefix, so serving it here would just
	// render broken.
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<!doctype html><html><head><title></title></head><body></body></html>"))
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
		api.POST("/admin/:username/users/disable", requireSudo, h.handleDisableAdminUsers)
		api.POST("/admin/:username/users/activate", requireSudo, h.handleActivateAdminUsers)
		api.POST("/admin/usage/reset/:username", requireSudo, h.handleResetAdminUsage)
		api.GET("/admin/usage/:username", requireSudo, h.handleGetAdminUsage)

		api.GET("/inbounds", requireAdmin, h.handleListInbounds)
		api.POST("/inbounds/sync", requireSudo, h.handleSyncInbounds)
		api.GET("/inbounds/detail", requireSudo, h.handleListInboundsDetailed)
		api.DELETE("/inbounds/:tag", requireSudo, h.handleDeleteInbound)
		api.POST("/inbounds/import-xray", requireSudo, h.handleImportXrayConfig)

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
		api.GET("/user/:username/usage", requireAdmin, h.handleGetUserUsage)
		api.POST("/user/:username/active-next", requireAdmin, h.handleActivateNextPlan)
		api.PUT("/user/:username/set-owner", requireSudo, h.handleSetUserOwner)

		api.GET("/users/usage", requireAdmin, h.handleGetUsersUsage)
		api.GET("/users/expired", requireAdmin, h.handleGetExpiredUsers)
		api.DELETE("/users/expired", requireAdmin, h.handleDeleteExpiredUsers)
		api.POST("/users/reset", requireSudo, h.handleResetAllUsersUsage)

		api.POST("/user_template", requireSudo, h.handleCreateUserTemplate)
		api.GET("/user_template", requireAdmin, h.handleListUserTemplates)
		api.GET("/user_template/:id", requireAdmin, h.handleGetUserTemplate)
		api.PUT("/user_template/:id", requireSudo, h.handleModifyUserTemplate)
		api.DELETE("/user_template/:id", requireSudo, h.handleDeleteUserTemplate)

		api.POST("/node", requireSudo, h.handleCreateNode)
		api.GET("/nodes", requireSudo, h.handleListNodes)
		api.GET("/nodes/usage", requireSudo, h.handleGetNodesUsage)
		api.GET("/node/settings", requireSudo, h.handleGetNodeSettings)
		api.GET("/node/:id", requireSudo, h.handleGetNode)
		api.PUT("/node/:id", requireSudo, h.handleUpdateNode)
		api.DELETE("/node/:id", requireSudo, h.handleDeleteNode)
		api.POST("/node/:id/reconnect", requireSudo, h.handleReconnectNode)

		api.GET("/monitoring", requireSudo, h.handleGetMonitoring)
		api.GET("/monitoring/history", requireSudo, h.handleGetMonitoringHistory)

		api.GET("/settings/integrations", requireSudo, h.handleGetIntegrationSettings)
		api.PUT("/settings/integrations", requireSudo, h.handleUpdateIntegrationSettings)

		api.GET("/settings/core-config", requireSudo, h.handleGetCoreConfig)
		api.PUT("/settings/core-config", requireSudo, h.handleUpdateCoreConfig)
		api.GET("/settings/xray-config", requireSudo, h.handleGetXrayConfig)
		api.PUT("/settings/xray-config", requireSudo, h.handleUpdateXrayConfig)

		// The panel's external-client API surface for the core - kept
		// separate from /settings/core-config above (this codebase's own
		// sing-box-flavored DTO) since external reseller bots
		// call these exact paths expecting real, raw Xray JSON. See
		// internal/httpapi/corexrayconfig.go's doc comments for scope.
		// Any admin, not just sudo - the API contract lets any logged-in
		// admin call this one while everything else under /core is
		// sudo-only. It matters: a reseller bot logged in as an
		// ordinary (non-sudo) admin probes this to decide whether the
		// panel is reachable at all, and a 403 here reads to it as the
		// whole server being down.
		api.GET("/core", requireAdmin, h.handleGetCoreVersion)
		// Readable by any admin, but a non-sudo one gets a secret-free copy
		// (see handleGetRawXrayConfig). Every WRITE below stays sudo-only.
		api.GET("/core/config", requireAdmin, h.handleGetRawXrayConfig)
		api.PUT("/core/config", requireSudo, h.handlePutRawXrayConfig)
		api.POST("/core/restart", requireSudo, h.handleRestartCore)
		api.POST("/core/config/validate", requireSudo, h.handleValidateRawXrayConfig)
		api.GET("/core/config/backups", requireSudo, h.handleListCoreConfigBackups)
		api.GET("/core/config/backups/:id", requireSudo, h.handleGetCoreConfigBackup)
		api.POST("/core/config/backups/:id/restore", requireSudo, h.handleRestoreCoreConfigBackup)

		api.GET("/settings/backup", requireSudo, h.handleListBackups)
		api.POST("/settings/backup", requireSudo, h.handleCreateBackup)
		api.GET("/settings/backup/:filename", requireSudo, h.handleDownloadBackup)
		api.DELETE("/settings/backup/:filename", requireSudo, h.handleDeleteBackup)
		api.POST("/settings/backup/:filename/restore", requireSudo, h.handleRestoreBackup)
		api.POST("/settings/backup/restore-upload", requireSudo, h.handleRestoreUpload)

		api.GET("/settings/gateway", requireSudo, h.handleGetGatewaySettings)
		api.PUT("/settings/gateway", requireSudo, h.handleUpdateGatewaySettings)
		api.GET("/settings/gateway/peers", requireSudo, h.handleListGatewayPeers)
		api.POST("/settings/gateway/peers", requireSudo, h.handleCreateGatewayPeer)
		api.PUT("/settings/gateway/peers/:id", requireSudo, h.handleUpdateGatewayPeer)
		api.DELETE("/settings/gateway/peers/:id", requireSudo, h.handleDeleteGatewayPeer)
		api.POST("/settings/gateway/peers/:id/test", requireSudo, h.handleTestGatewayPeer)

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

		// Panel -> panel (Gateway / multi-panel load balancer), never an
		// admin JWT - authenticated by this panel's own gateway secret
		// (h.requireGatewaySecret), the same shape as requireNodeSecret
		// above but checked against gateway_settings instead of a specific
		// node's report_secret. See internal/httpapi/gateway.go.
		api.GET("/internal/gateway/ping", h.requireGatewaySecret, h.handleGatewayPing)
		api.POST("/internal/gateway/users/sync", h.requireGatewaySecret, h.handleGatewaySyncUser)
		api.GET("/internal/gateway/status", h.requireGatewaySecret, h.handleGatewayStatus)
	}

	r.GET("/sub/:token", h.handleGetSubscription)
	r.GET("/sub/:token/:format", h.handleGetSubscriptionFormat)
	r.GET("/sub/:token/info", h.handleSubscriptionInfo)
	r.GET("/sub/:token/usage", h.handleGetSubscriptionUsage)
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
