package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/auth"
	"github.com/legendary1205/rapido-go/internal/proxysettings"
	"github.com/legendary1205/rapido-go/internal/xrayimport"
)

// xrayCoreVersionReported is a fixed, plausible Xray-core version string -
// there is no real Xray process running behind this panel (nodes run
// sing-box), so there is no real version to report. A reseller bot only
// ever displays this field; it isn't used to gate behavior anywhere in
// the external clients this endpoint keeps compatibility with.
const xrayCoreVersionReported = "1.8.24"

// minNodeVersionReported mirrors NodeSettings.min_node_version's own
// default on the real panel - a value its dashboard shows next to the CA
// certificate when adding a node, not something either side enforces.
const minNodeVersionReported = "v0.2.0"

// handleGetCoreVersion implements GET /api/core (sudo only) - the panel
// API's core-stats endpoint. Kept deliberately tiny: every external
// reseller/management bot that calls this only reads it to show an admin
// "core is running", never to gate a later request.
func (h *Handler) handleGetCoreVersion(c *gin.Context) {
	inboundRows, err := h.store.Queries.ListAutoSyncInbounds(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read core status"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"version": xrayCoreVersionReported,
		"started": len(inboundRows) > 0,
		// The panel API's core-stats model carries this third field (the
		// path of its live log websocket). A client that reads it and connects
		// would find nothing here - this architecture has no equivalent
		// stream - but omitting the key entirely breaks a strict client
		// that expects the full model, which costs more than an unused
		// path does.
		"logs_websocket": "/api/core/logs",
	})
}

// handleRestartCore implements POST /api/core/restart (sudo only). The
// API's contract is to push a freshly-rendered config to the core and every
// connected node here; this architecture inverts that - nodes pull their
// own config on a short interval (see handleGetNodeConfig) - so the honest equivalent
// is to drop the cached fleet-wide payload, which makes every node rebuild
// from current data on its very next poll instead of up to the cache TTL
// later. Returns {} exactly as the real endpoint does.
func (h *Handler) handleRestartCore(c *gin.Context) {
	if err := h.store.InvalidateNodeConfigPayload(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not trigger a core restart"})
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}

// handlePutRawXrayConfig implements PUT /api/core/config (sudo only) - the
// write half of the endpoint handleGetRawXrayConfig reads. Takes a real,
// raw Xray JSON document (the same shape a bot just read back), translates
// it through the same importer POST /api/inbounds/import-xray uses, and
// echoes the payload on success, matching the real panel's own contract.
func (h *Handler) handlePutRawXrayConfig(c *gin.Context) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Could not read the request body"})
		return
	}
	parsed, err := xrayimport.ParseXrayConfig(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "could not parse this as an Xray config: " + err.Error()})
		return
	}
	if badRequest, err := h.applyParsedXrayConfig(c.Request.Context(), parsed); err != nil {
		status := http.StatusInternalServerError
		if badRequest {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"detail": err.Error()})
		return
	}
	// Echo the payload back, byte for byte, exactly as the real endpoint
	// does ("On success the response body is unchanged: the payload").
	c.Data(http.StatusOK, "application/json", raw)
}

// handleValidateRawXrayConfig implements POST /api/core/config/validate
// (sudo only): structural checks only, writing and restarting nothing.
// checked_with_xray is always false here and that is not a placeholder -
// there is no Xray binary in this stack to hand the file to, so claiming
// otherwise would be a lie a dashboard would act on.
func (h *Handler) handleValidateRawXrayConfig(c *gin.Context) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Could not read the request body"})
		return
	}
	parsed, err := xrayimport.ParseXrayConfig(raw)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"valid": false, "detail": err.Error(), "checked_with_xray": false,
		})
		return
	}
	detail := "Configuration is structurally valid."
	if len(parsed.Warnings) > 0 {
		detail = "Configuration is usable, with warnings: " + strings.Join(parsed.Warnings, "; ")
	}
	c.JSON(http.StatusOK, gin.H{
		"valid": true, "detail": detail, "checked_with_xray": false,
	})
}

// handleListCoreConfigBackups implements GET /api/core/config/backups
// (sudo only). The API contract is a list of timestamped copies of the core
// config kept on disk; this panel has no such file to copy (its core config
// lives in Postgres, and whole-database backups are their own endpoint family under
// /api/settings/backup), so the honest answer is an empty list rather than
// a 404 a dashboard would render as an error.
func (h *Handler) handleListCoreConfigBackups(c *gin.Context) {
	c.JSON(http.StatusOK, []any{})
}

// handleGetCoreConfigBackup implements GET /api/core/config/backups/:id.
// Nothing is ever stored (see handleListCoreConfigBackups), so every id is
// genuinely absent.
func (h *Handler) handleGetCoreConfigBackup(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"detail": "Backup not found"})
}

// handleRestoreCoreConfigBackup implements
// POST /api/core/config/backups/:id/restore - same reasoning as above.
func (h *Handler) handleRestoreCoreConfigBackup(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"detail": "Backup not found"})
}

// handleGetNodeSettings implements GET /api/node/settings (sudo only) -
// the CA certificate a new node has to trust, which this panel issues
// every node's own leaf certificate from (see handleCreateNode).
func (h *Handler) handleGetNodeSettings(c *gin.Context) {
	tls, err := h.store.Queries.GetTLS(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read node settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"min_node_version": minNodeVersionReported,
		"certificate":      tls.Certificate,
	})
}

// handleReconnectNode implements POST /api/node/:id/reconnect (sudo only).
// The API contract has the panel dial the node itself here; this
// architecture has no panel-to-node channel at all (nodes poll), so the equivalent is to drop
// the cached config payload so that node's very next poll rebuilds from
// current data. Returns the same body the real endpoint does.
func (h *Handler) handleReconnectNode(c *gin.Context) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"detail": "Invalid node id"})
		return
	}
	ctx := c.Request.Context()
	if _, err := h.store.Queries.GetNodeByID(ctx, id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Node not found"})
		return
	}
	if err := h.store.InvalidateNodeConfigPayload(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not schedule a reconnection"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detail": "Reconnection task scheduled"})
}

// buildRawXrayInbounds gathers this panel's own auto-sync inbounds and
// active-user proxy credentials - the exact same two O(1) queries
// buildNodeConfigPayload (internal/httpapi/nodeconfig.go) already uses for
// every node's own config pull, proven at ~9800-user fleet scale - and
// reshapes them into xrayimport.ExportInbound, the input BuildXrayJSON
// needs to render a real Xray JSON document. Every non-excluded user of a
// given protocol is attached to every inbound of that protocol, matching
// buildNodeConfigPayload's own "include_db_users()" behavior exactly
// (including its same known gap: no per-user inbound exclusion - see that
// file's doc comment).
//
// An inbound whose hosts span several ports is exported as ONE Xray inbound
// with a comma-separated "port"; PUT /api/core/config still splits such an
// inbound back into one panel inbound per port (xrayimport.ParseXrayConfig).
func (h *Handler) buildRawXrayInbounds(ctx context.Context) ([]xrayimport.ExportInbound, error) {
	inboundRows, err := h.store.Queries.ListAutoSyncInbounds(ctx)
	if err != nil {
		return nil, err
	}
	proxyRows, err := h.store.Queries.ListActiveUserProxiesForNodeConfig(ctx)
	if err != nil {
		return nil, err
	}

	clientsByProtocol := make(map[string][]xrayimport.ExportClient)
	for _, p := range proxyRows {
		settings, err := proxysettings.FromStored(proxysettings.ProxyType(p.Type), p.Settings)
		if err != nil {
			continue // a malformed row shouldn't take down every other user's config
		}
		client := xrayimport.ExportClient{Email: p.Username}
		switch p.Type {
		case "vmess":
			client.UUID = settings.VMess.ID
		case "vless":
			client.UUID = settings.VLESS.ID
			client.Flow = string(settings.VLESS.Flow)
		case "trojan":
			client.Password = settings.Trojan.Password
		case "shadowsocks":
			client.Password = settings.Shadowsocks.Password
			client.Method = string(settings.Shadowsocks.Method)
		default:
			continue
		}
		clientsByProtocol[p.Type] = append(clientsByProtocol[p.Type], client)
	}

	inbounds := make([]xrayimport.ExportInbound, 0, len(inboundRows))
	for _, in := range inboundRows {
		inbounds = append(inbounds, xrayimport.ExportInbound{
			Tag: in.Tag, Protocol: in.Protocol, Network: in.Network, HeaderType: in.HeaderType.String, Security: in.Security,
			RealityPrivateKey: in.RealityPrivateKey.String, RealityShortIDs: in.RealityShortIds,
			RealityServerName: in.RealityServerName.String, RealityServerPort: in.RealityServerPort.Int32,
			TLSCertificate: in.TlsCertificate.String, TLSKey: in.TlsKey.String, TLSServerName: in.TlsServerName.String,
			Port: in.Port.Int32, Ports: validHostPorts(in.Ports), SNI: in.Sni.String, Host: in.Host.String,
			Clients: clientsByProtocol[in.Protocol],
		})
	}
	return inbounds, nil
}

func coreOutboundsToExport(in []outboundDTO) []xrayimport.Outbound {
	out := make([]xrayimport.Outbound, len(in))
	for i, ob := range in {
		out[i] = xrayimport.Outbound{
			Tag: ob.Tag, Type: ob.Type, Server: ob.Server, ServerPort: ob.ServerPort,
			Username: ob.Username, Password: ob.Password, Outbounds: ob.Outbounds,
			UUID: ob.UUID, Flow: ob.Flow, Method: ob.Method, Security: ob.Security, CongestionControl: ob.CongestionControl,
			TLSEnabled: ob.TLSEnabled, TLSServerName: ob.TLSServerName, TLSInsecure: ob.TLSInsecure,
			BindInterface: ob.BindInterface,
		}
	}
	return out
}

func coreRoutingRulesToExport(in []routingRuleDTO) []xrayimport.RoutingRule {
	out := make([]xrayimport.RoutingRule, len(in))
	for i, r := range in {
		out[i] = xrayimport.RoutingRule{
			Inbound: r.Inbound, InboundPort: r.InboundPort, Domain: r.Domain, DomainSuffix: r.DomainSuffix, DomainKeyword: r.DomainKeyword,
			IPCIDR: r.IPCIDR, IPIsPrivate: r.IPIsPrivate, Port: r.Port, PortRange: r.PortRange,
			Network: r.Network, Protocol: r.Protocol, OutboundTag: r.OutboundTag,
		}
	}
	return out
}

func coreDNSServersToExport(in []dnsServerDTO) []xrayimport.DNSServer {
	out := make([]xrayimport.DNSServer, len(in))
	for i, d := range in {
		out[i] = xrayimport.DNSServer{Tag: d.Tag, Type: d.Type, Address: d.Address, Port: d.Port, Path: d.Path}
	}
	return out
}

// handleGetRawXrayConfig implements GET /api/core/config (sudo only) -
// the one route this whole feature exists for: the exact call an external
// reseller/management bot makes to read a panel's live Xray config.
// Returns a real, raw Xray JSON document - not this codebase's own
// sing-box-flavored Core Config DTO - built fresh on every call from the same live data every node
// itself is configured from, so it always reflects the current fleet.
func (h *Handler) handleGetRawXrayConfig(c *gin.Context) {
	ctx := c.Request.Context()
	inbounds, err := h.buildRawXrayInbounds(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read inbounds"})
		return
	}

	// A non-sudo admin gets the same document with every secret stripped.
	//
	// The real panel simply refuses this route to non-sudo admins, and the
	// reason is sound: the full config carries each inbound's TLS and
	// REALITY private keys plus the UUID/password of EVERY user on the
	// fleet. But refusing it outright has a worse real-world consequence -
	// an external reseller bot (confirmed in its own source: it reads the
	// inbounds list purely for tag+protocol when building a plan) then shows the operator an empty inbound list, and the usual
	// fix is to make that reseller a sudo admin, which hands them the real
	// keys AND full control of the panel.
	//
	// Serving a redacted copy gives the bot exactly the two fields it
	// reads while leaking nothing, and leaves sudo behaviour untouched.
	identity := auth.CurrentIdentity(c)
	redacted := identity != nil && !identity.IsSudo
	if redacted {
		for i := range inbounds {
			inbounds[i].Clients = nil
			inbounds[i].TLSCertificate = ""
			inbounds[i].TLSKey = ""
			inbounds[i].RealityPrivateKey = ""
			inbounds[i].RealityShortIDs = nil
		}
	}
	coreRow, err := h.store.CachedGetCoreConfig(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read core config"})
		return
	}
	core := toCoreConfigDTO(coreRow)
	if redacted {
		// Outbounds carry upstream credentials (a vmess/trojan exit's own
		// uuid/password) and the fleet's exit topology; routing and DNS
		// describe where traffic goes. None of it is anything a reseller's
		// bot reads, so none of it is served to one.
		core.Outbounds, core.RoutingRules, core.DNSServers = nil, nil, nil
	}

	raw, err := xrayimport.BuildXrayJSON(
		inbounds,
		coreOutboundsToExport(core.Outbounds),
		coreRoutingRulesToExport(core.RoutingRules),
		coreDNSServersToExport(core.DNSServers),
		core.LogLevel,
		core.SniffEnabled,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not build xray config"})
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}
