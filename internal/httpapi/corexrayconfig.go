package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/proxysettings"
	"github.com/legendary1205/rapido-go/internal/xrayimport"
)

// xrayCoreVersionReported is a fixed, plausible Xray-core version string -
// there is no real Xray process running behind this panel (nodes run
// sing-box), so there is no real version to report. A reseller bot only
// ever displays this field; it isn't used to gate behavior anywhere in
// the real Marzban ecosystem this is restoring compatibility with.
const xrayCoreVersionReported = "1.8.24"

// handleGetCoreVersion implements GET /api/core (sudo only) - real
// Marzban's app/routers/core.py CoreStats endpoint. Kept deliberately
// tiny: every genuine-Marzban-API reseller bot that calls this (WizWiz's
// config.php included) only reads it to show an admin "core is running",
// never to gate a later request.
func (h *Handler) handleGetCoreVersion(c *gin.Context) {
	inboundRows, err := h.store.Queries.ListAutoSyncInbounds(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read core status"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"version": xrayCoreVersionReported,
		"started": len(inboundRows) > 0,
	})
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
			Port: in.Port.Int32, SNI: in.Sni.String, Host: in.Host.String,
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
			Inbound: r.Inbound, Domain: r.Domain, DomainSuffix: r.DomainSuffix, DomainKeyword: r.DomainKeyword,
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
// the one route this whole feature exists for: real Marzban's
// app/routers/core.py get_core_config, the exact call a genuine-Marzban-
// API reseller bot makes to read a panel's live Xray config (confirmed
// against wizwizdev/wizwizxui-timebot's config.php, whose misleadingly-
// named getMarzbanHosts() is this request). Returns a real, raw Xray
// JSON document - not this codebase's own sing-box-flavored Core Config
// DTO - built fresh on every call from the same live data every node
// itself is configured from, so it always reflects the current fleet.
func (h *Handler) handleGetRawXrayConfig(c *gin.Context) {
	ctx := c.Request.Context()
	inbounds, err := h.buildRawXrayInbounds(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read inbounds"})
		return
	}
	coreRow, err := h.store.CachedGetCoreConfig(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read core config"})
		return
	}
	core := toCoreConfigDTO(coreRow)

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
