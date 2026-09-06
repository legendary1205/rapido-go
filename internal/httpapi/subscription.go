package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
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

	format := "v2ray"
	if isSingBoxUserAgent(c.GetHeader("User-Agent")) {
		format = "sing-box"
	}
	h.writeSubscription(c, user, format, true)
}

// handleGetSubscriptionFormat implements GET /sub/:token/:format - an
// explicit format request, bypassing User-Agent sniffing entirely. Does
// NOT update sub_updated_at/sub_last_user_agent, matching the current
// system's explicit-client_type route.
func (h *Handler) handleGetSubscriptionFormat(c *gin.Context) {
	user, ok := h.loadSubscriptionUser(c)
	if !ok {
		return
	}
	h.writeSubscription(c, user, c.Param("format"), false)
}

func isSingBoxUserAgent(ua string) bool {
	ua = strings.ToLower(ua)
	for _, needle := range []string{"sing-box", "sfa", "sfi", "sfm", "sft", "karing", "hiddifynext"} {
		if strings.Contains(ua, needle) {
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
// response headers real client apps read for usage/expiry display.
func (h *Handler) writeSubscription(c *gin.Context, user generated.User, format string, recordUserAgent bool) {
	ctx := c.Request.Context()
	proxies, err := h.store.Queries.ListProxiesByUserID(ctx, pgInt4FromInt(int(user.ID)))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not read proxies"})
		return
	}

	vars := subscription.BuildVariables(toSubUserInfo(user), h.publicIP)

	var links []string
	var singboxOutbounds []map[string]any

	for _, p := range proxies {
		settings, err := proxysettings.FromStored(proxysettings.ProxyType(p.Type), p.Settings)
		if err != nil {
			continue
		}
		known, err := h.store.CachedListInboundTagsByProtocol(ctx, p.Type)
		if err != nil {
			continue
		}
		excluded, err := h.store.CachedListExcludedInboundTags(ctx, p.ID)
		if err != nil {
			continue
		}
		includedTags := subtractTags(known, excluded)

		for _, tag := range includedTags {
			inbound, err := h.store.CachedGetInboundByTag(ctx, tag)
			if err != nil {
				continue
			}
			hosts, err := h.store.CachedListHostsByInboundTag(ctx, tag)
			if err != nil {
				continue
			}
			for _, host := range hosts {
				eff := subscription.BuildEffectiveInbound(inbound, host)
				remarkVars := vars
				remarkVars["PROTOCOL"] = p.Type
				remarkVars["TRANSPORT"] = eff.Network
				remark := remarkVars.Format(host.Remark)
				address := remarkVars.Format(host.Address)

				switch format {
				case "sing-box":
					out, err := subscription.SingBoxOutbound(remark, address, eff, settings)
					if err == nil && out != nil {
						singboxOutbounds = append(singboxOutbounds, out)
					}
				default:
					link, err := subscription.BuildLink(remark, address, eff, settings)
					if err == nil {
						links = append(links, link)
					}
				}
			}
		}
	}

	h.setSubscriptionHeaders(c, user)
	if recordUserAgent {
		h.recordSubUserAgent(ctx, user.ID, c.GetHeader("User-Agent"))
	}

	switch format {
	case "sing-box":
		raw, err := subscription.SingBoxConfig(singboxOutbounds)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Could not build sing-box config"})
			return
		}
		c.Data(http.StatusOK, "application/json", raw)
	default:
		c.String(http.StatusOK, base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n"))))
	}
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
