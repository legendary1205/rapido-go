package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/proxysettings"
	"github.com/legendary1205/rapido-go/internal/subscription"
)

// handleGetSubscription implements GET /sub/:token - auto-detects the
// client format from User-Agent, matching subscription.py's routing table.
// Only sing-box (via a "sing-box"/"SFA"/etc. substring) is detected so far;
// everything else - including Clash, which this phase hasn't implemented a
// generator for yet - falls back to the universal v2ray share-link format,
// the same default the current Python system uses for any unmatched
// User-Agent.
func (h *Handler) handleGetSubscription(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}

	// Content negotiation is purely on Accept, not User-Agent - matches
	// app/routers/subscription.py exactly. A browser requests text/html and
	// gets the customer-facing Overview/Apps/Servers/Support page; every
	// VPN client app asks for something else (usually */* or a specific
	// config mime type) and falls through to the raw-config branches below.
	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		h.handleSubscriptionPage(c, user)
		return
	}

	h.writeSubscription(c, user, detectSubscriptionFormat(c.GetHeader("User-Agent")), true)
}

// handleGetSubscriptionFormat implements GET /sub/:token/:format - an
// explicit format request, bypassing User-Agent sniffing entirely. Does
// NOT update sub_updated_at/sub_last_user_agent, matching the current
// system's explicit-client_type route. Rejects an unrecognized format with
// 404 rather than silently falling back to v2ray links - the app/routers/
// subscription.py original enforces the same whitelist via a Path regex
// (`sing-box|clash-meta|clash|outline|v2ray|v2ray-json`); this used to
// accept any string here and always fall through to v2ray links with 200,
// found via this project's own live stress-testing audit.
func (h *Handler) handleGetSubscriptionFormat(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}
	format := c.Param("format")
	if !validSubscriptionFormats[format] {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Not Found"})
		return
	}
	h.writeSubscription(c, user, format, false)
}

var validSubscriptionFormats = map[string]bool{
	"v2ray": true, "sing-box": true, "clash": true, "clash-meta": true, "outline": true, "v2ray-json": true,
}

// detectSubscriptionFormat auto-selects a format from the client's real
// User-Agent, porting app/routers/subscription.py's own regex chain (checked
// in this exact order - Clash Meta forks are matched before plain Clash,
// since "ClashMetaForAndroid" etc. would otherwise match the plainer Clash
// pattern first). Deliberately simplified in one place, documented rather
// than silently dropped: the Python original version-gates v2rayN/v2rayNG/
// Streisand/Happ's move to v2ray-json behind per-client USE_CUSTOM_JSON_*
// settings and, for a narrow v2rayNG version band, reverses the link order
// as a workaround for a bug in that specific release range - this project
// has no equivalent of either knob yet, so those clients always get
// v2ray-json (a strict upgrade over the v2ray-link fallback they'd otherwise
// silently get) with no version check and no reversal.
func detectSubscriptionFormat(userAgent string) string {
	lower := strings.ToLower(userAgent)
	switch {
	case hasAnyPrefix(lower, "clash-verge", "clash-meta", "clash.meta", "flclash", "mihomo"):
		return "clash-meta"
	case hasAnyPrefix(lower, "clash", "stash"):
		return "clash"
	// Only the sing-box family's own name is a real substring match in the
	// Python original (`.*sing[-b]?ox.*`, unanchored) - every other branch
	// here is anchored at the start of the User-Agent, same as Python's `^`.
	case hasAnyPrefix(lower, "sfa", "sfi", "sfm", "sft", "karing", "hiddifynext") ||
		strings.Contains(lower, "singbox") || strings.Contains(lower, "sing-box"):
		return "sing-box"
	case hasAnyPrefix(lower, "ss", "ssr", "ssd", "sss", "outline", "shadowsocks", "ssconf"):
		return "outline"
	case hasAnyPrefix(lower, "v2rayn", "v2rayng", "streisand", "happ", "ktor-client"):
		return "v2ray-json"
	default:
		return "v2ray"
	}
}

func hasAnyPrefix(lowerUserAgent string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(lowerUserAgent, p) {
			return true
		}
	}
	return false
}

// loadSubscriptionUser validates the token (internal/subscription's
// bespoke signed-timestamp scheme, not a JWT) and re-fetches the user,
// enforcing the same invalidation rule as get_validated_sub: a token whose
// embedded timestamp predates the user's created_at or sub_revoked_at is
// rejected, even though the token itself never expires on its own.
func (h *Handler) loadSubscriptionUser(c *gin.Context) (generated.User, bool) {
	username, createdAt, ok := subscription.ValidateToken(c.Param("token"), h.jwtSecret)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Not Found"})
		return generated.User{}, false
	}
	user, err := h.store.Queries.GetUserByUsername(c.Request.Context(), username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Not Found"})
		return generated.User{}, false
	}
	if user.CreatedAt.Valid && user.CreatedAt.Time.After(createdAt) {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Not Found"})
		return generated.User{}, false
	}
	if user.SubRevokedAt.Valid && user.SubRevokedAt.Time.After(createdAt) {
		c.JSON(http.StatusNotFound, gin.H{"detail": "Not Found"})
		return generated.User{}, false
	}
	return user, true
}

// writeSubscription builds every link/config for the user's proxies+hosts
// and renders it in the requested format, setting the SIP-subscription
// response headers real client apps read for usage/expiry display. Content
// types match app/routers/subscription.py's own client_config table
// (text/yaml for the two Clash formats, application/json for sing-box/
// outline/v2ray-json, text/plain base64 for v2ray links - the one format
// that isn't already a structured document).
func (h *Handler) writeSubscription(c *gin.Context, user generated.User, format string, recordUserAgent bool) {
	ctx := c.Request.Context()

	var raw []byte
	var links []string
	var err error
	switch format {
	case "sing-box":
		var outbounds []map[string]any
		if outbounds, err = h.buildUserSingBoxOutbounds(ctx, user); err == nil {
			raw, err = subscription.SingBoxConfig(outbounds)
		}
	case "clash", "clash-meta":
		raw, err = h.buildUserClashConfig(ctx, user, format == "clash-meta")
	case "outline":
		raw, err = h.buildUserOutlineConfig(ctx, user)
	case "v2ray-json":
		raw, err = h.buildUserV2rayJSONConfig(ctx, user)
	default: // v2ray
		links, err = h.buildUserLinks(ctx, user)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read proxies"})
		return
	}

	h.setSubscriptionHeaders(c, user)
	if recordUserAgent {
		h.recordSubUserAgent(ctx, user.ID, c.GetHeader("User-Agent"))
	}

	switch format {
	case "sing-box", "outline", "v2ray-json":
		c.Data(http.StatusOK, "application/json", raw)
	case "clash", "clash-meta":
		c.Data(http.StatusOK, "text/yaml", raw)
	default: // v2ray
		c.String(http.StatusOK, base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n"))))
	}
}

// forEachUserHost walks proxy -> host (across every included inbound tag,
// in one globally-sorted sequence), exactly the traversal writeSubscription
// always needed - factored out so buildUserLinks/buildUserSingBoxOutbounds
// (and the HTML page's Servers tab, via buildUserLinks) don't each
// re-implement it.
//
// Hosts are gathered from every included tag first and THEN sorted once by
// (priority, id) globally, rather than emitted tag-by-tag - this is what
// lets an admin interleave configs from different inbound tags/nodes into
// one chosen sequence (see hosts.priority, migration 00008) instead of
// always seeing every tag's hosts grouped together in alphabetical-tag
// order. Each generated.Host already carries its own InboundTag column, so
// no second lookup is needed to know which tag a gathered host came from.
func (h *Handler) forEachUserHost(ctx context.Context, user generated.User, fn func(protocol string, settings proxysettings.Settings, remark, address string, eff subscription.EffectiveInbound)) error {
	proxies, err := h.store.Queries.ListProxiesByUserID(ctx, pgInt4FromInt(int(user.ID)))
	if err != nil {
		return err
	}
	vars := subscription.BuildVariables(toSubUserInfo(user), h.publicIP)
	settingsByProtocol := make(map[string]proxysettings.Settings, len(proxies))

	for _, p := range proxies {
		settings, err := proxysettings.FromStored(proxysettings.ProxyType(p.Type), p.Settings)
		if err != nil {
			continue
		}
		settingsByProtocol[p.Type] = settings
		known, err := h.store.CachedListInboundTagsByProtocol(ctx, p.Type)
		if err != nil {
			continue
		}
		excluded, err := h.store.CachedListExcludedInboundTags(ctx, p.ID)
		if err != nil {
			continue
		}
		includedTags := subtractTags(known, excluded)

		var allHosts []generated.Host
		for _, tag := range includedTags {
			hosts, err := h.store.CachedListHostsByInboundTag(ctx, tag)
			if err != nil {
				continue
			}
			allHosts = append(allHosts, hosts...)
		}
		sort.Slice(allHosts, func(i, j int) bool {
			if allHosts[i].Priority != allHosts[j].Priority {
				return allHosts[i].Priority < allHosts[j].Priority
			}
			return allHosts[i].ID < allHosts[j].ID
		})

		for _, host := range allHosts {
			inbound, err := h.store.CachedGetInboundByTag(ctx, host.InboundTag)
			if err != nil {
				continue
			}
			eff := subscription.BuildEffectiveInbound(inbound, host)
			remarkVars := vars
			remarkVars["PROTOCOL"] = p.Type
			remarkVars["TRANSPORT"] = eff.Network
			remark := remarkVars.Format(host.Remark)
			address := remarkVars.Format(host.Address)
			fn(p.Type, settings, remark, address, eff)
		}
	}

	// Gateway (multi-panel load balancer) sub-phase 4: append every
	// configured peer's cached hosts, strictly AFTER every local host above
	// - see gatherPeerHosts's own doc comment for why this never reorders
	// or interleaves with the admin's own local priority ordering. Reuses
	// the exact same fn(...) callback local hosts use, so buildUserLinks/
	// buildUserSingBoxOutbounds need no changes at all to pick these up.
	for _, ph := range h.gatherPeerHosts(ctx, settingsByProtocol) {
		settings, ok := settingsByProtocol[ph.host.Protocol]
		if !ok {
			// gatherPeerHosts already filters to protocols this user has
			// locally, so this can't actually happen - guarded anyway
			// since fn's settings argument must never be a zero value.
			continue
		}
		remarkVars := vars
		remarkVars["PROTOCOL"] = ph.host.Protocol
		remarkVars["TRANSPORT"] = ph.host.Network
		remark := "[" + ph.peerName + "] " + remarkVars.Format(ph.host.Remark)
		address := remarkVars.Format(ph.host.Address)
		eff := subscription.EffectiveInbound{
			Tag: ph.host.Tag, Protocol: ph.host.Protocol, Network: ph.host.Network, HeaderType: ph.host.HeaderType,
			Port: ph.host.Port, Address: ph.host.Address, SNI: ph.host.SNI, HostHeader: ph.host.HostHeader,
			Path: ph.host.Path, Security: ph.host.Security, ALPN: ph.host.ALPN, Fingerprint: ph.host.Fingerprint,
			AllowInsecure: ph.host.AllowInsecure, RealityPublicKey: ph.host.RealityPublicKey, RealityShortID: ph.host.RealityShortID,
			MuxEnable: ph.host.MuxEnable, FragmentSetting: ph.host.FragmentSetting, NoiseSetting: ph.host.NoiseSetting,
			RandomUserAgent: ph.host.RandomUserAgent,
		}
		fn(ph.host.Protocol, settings, remark, address, eff)
	}
	return nil
}

// buildUserLinks is the v2ray-share-link list for a user - both the raw
// v2ray-format subscription endpoint and the HTML page's Servers tab
// (embedded once at page load, matching Python's user.links field) use
// this exact same list, never two separately-generated ones.
func (h *Handler) buildUserLinks(ctx context.Context, user generated.User) ([]string, error) {
	var links []string
	err := h.forEachUserHost(ctx, user, func(protocol string, settings proxysettings.Settings, remark, address string, eff subscription.EffectiveInbound) {
		link, err := subscription.BuildLink(remark, address, eff, settings)
		if err == nil {
			links = append(links, link)
		}
	})
	return links, err
}

func (h *Handler) buildUserSingBoxOutbounds(ctx context.Context, user generated.User) ([]map[string]any, error) {
	var outbounds []map[string]any
	err := h.forEachUserHost(ctx, user, func(protocol string, settings proxysettings.Settings, remark, address string, eff subscription.EffectiveInbound) {
		out, err := subscription.SingBoxOutbound(remark, address, eff, settings)
		if err == nil && out != nil {
			outbounds = append(outbounds, out)
		}
	})
	return outbounds, err
}

func (h *Handler) buildUserClashConfig(ctx context.Context, user generated.User, isMeta bool) ([]byte, error) {
	var proxies []map[string]any
	err := h.forEachUserHost(ctx, user, func(protocol string, settings proxysettings.Settings, remark, address string, eff subscription.EffectiveInbound) {
		node, err := subscription.ClashProxy(remark, address, eff, settings, isMeta)
		if err == nil && node != nil {
			proxies = append(proxies, node)
		}
	})
	if err != nil {
		return nil, err
	}
	return subscription.ClashConfig(proxies)
}

func (h *Handler) buildUserOutlineConfig(ctx context.Context, user generated.User) ([]byte, error) {
	var servers []any
	index := 0
	err := h.forEachUserHost(ctx, user, func(protocol string, settings proxysettings.Settings, remark, address string, eff subscription.EffectiveInbound) {
		server, err := subscription.OutlineServer(subscription.OutlineServerID(index), remark, address, eff, settings)
		if err == nil && server != nil {
			servers = append(servers, server)
			index++
		}
	})
	if err != nil {
		return nil, err
	}
	return subscription.OutlineConfig(servers)
}

func (h *Handler) buildUserV2rayJSONConfig(ctx context.Context, user generated.User) ([]byte, error) {
	var configs []map[string]any
	err := h.forEachUserHost(ctx, user, func(protocol string, settings proxysettings.Settings, remark, address string, eff subscription.EffectiveInbound) {
		cfg, err := subscription.V2rayJSONConfig(remark, address, eff, settings)
		if err == nil && cfg != nil {
			configs = append(configs, cfg)
		}
	})
	if err != nil {
		return nil, err
	}
	return subscription.V2rayJSONArray(configs)
}

func (h *Handler) setSubscriptionHeaders(c *gin.Context, user generated.User) {
	total := int64(0)
	if user.DataLimit.Valid {
		total = user.DataLimit.Int64
	}
	expire := int64(0)
	if user.Expire.Valid {
		expire = int64(user.Expire.Int32)
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, user.Username))
	c.Header("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(user.Username)))
	c.Header("Profile-Update-Interval", "12")
	c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", user.UsedTraffic, total, expire))
}

// recordSubUserAgent mirrors crud.update_user_sub, called on every hit of
// the auto-detect subscription route. Errors are logged, not surfaced to
// the client - a failed usage-tracking write shouldn't break the actual
// subscription response the client is waiting on.
func (h *Handler) recordSubUserAgent(ctx context.Context, userID int32, userAgent string) {
	if err := h.store.Queries.UpdateUserSub(ctx, generated.UpdateUserSubParams{
		ID: userID, SubLastUserAgent: textFromPtr(&userAgent),
	}); err != nil {
		h.logger.Error("could not record subscription user agent", "user_id", userID, "error", err)
	}
}

func toSubUserInfo(u generated.User) subscription.UserInfo {
	info := subscription.UserInfo{Username: u.Username, Status: u.Status, UsedTraffic: u.UsedTraffic}
	if u.DataLimit.Valid {
		info.DataLimit = &u.DataLimit.Int64
	}
	if u.Expire.Valid {
		e := int64(u.Expire.Int32)
		info.Expire = &e
	}
	if u.Status == "on_hold" {
		info.OnHold = true
		if u.OnHoldExpireDuration.Valid {
			info.OnHoldDuration = &u.OnHoldExpireDuration.Int64
		}
	}
	return info
}
