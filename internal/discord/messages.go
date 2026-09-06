package discord

import (
	"fmt"
	"strings"
	"time"

	"github.com/legendary1205/rapido-go/internal/subscription"
)

// belongsToText mirrors app/discord/handlers/report.py's
// `admin.username if admin else None` fed into an f-string - Python
// stringifies None as the literal text "None" there, preserved here for
// the same reason as telegram.belongsToText.
func belongsToText(belongsTo *string) string {
	if belongsTo == nil {
		return "None"
	}
	return *belongsTo
}

func formatDataLimit(limit *int64) string {
	if limit == nil || *limit == 0 {
		return "Unlimited"
	}
	return subscription.ReadableSize(*limit)
}

func formatExpire(expire *int64) string {
	if expire == nil || *expire == 0 {
		return "Never"
	}
	// Local server time, matching Python's naive datetime.fromtimestamp.
	return time.Unix(*expire, 0).Format("15:04:05 2006-01-02")
}

func joinProxies(proxies []string) string {
	return strings.Join(proxies, ", ")
}

var statusLabels = map[string]string{
	"active":   "**:white_check_mark: Activated**",
	"disabled": "**:x: Disabled**",
	"limited":  "**:low_battery: #Limited**",
	"expired":  "**:clock5: #Expired**",
	// Fixed vs. Python: no on_hold entry there either (same KeyError gap as
	// telegram.statusLabels) - see that package's comment for detail.
	"on_hold": "**:electric_plug: #OnHold**",
}

var statusColors = map[string]int{
	"active":   0x9ae6b4,
	"disabled": 0x424b59,
	"limited":  0xf8a7a8,
	"expired":  0xfbd38d,
	"on_hold":  0x90cdf4,
}

func StatusChangePayload(username, status string, belongsTo *string) EmbedPayload {
	label, ok := statusLabels[status]
	if !ok {
		label = "**#" + status + "**"
	}
	color, ok := statusColors[status]
	if !ok {
		color = 0x424b59
	}
	return EmbedPayload{Embeds: []Embed{{
		Description: fmt.Sprintf("%s\n----------------------\n**Username:** %s", label, username),
		Color:       color,
		Footer:      &Footer{Text: "Belongs To: " + belongsToText(belongsTo)},
	}}}
}

func NewUserPayload(username, byUsername string, dataLimit, expire *int64, proxies []string, hasNextPlan bool, resetStrategy string, belongsTo *string) EmbedPayload {
	return EmbedPayload{Embeds: []Embed{{
		Title: ":new: Created",
		Description: fmt.Sprintf(
			"\n**Username:** %s\n**Traffic Limit:** %s\n**Expire Date:** %s\n**Proxies:** %s\n**Data Limit Reset Strategy:**%s\n**Has Next Plan:**%t",
			username, formatDataLimit(dataLimit), formatExpire(expire), joinProxies(proxies), resetStrategy, hasNextPlan,
		),
		Footer: &Footer{Text: fmt.Sprintf("Belongs To: %s\nBy: %s", belongsToText(belongsTo), byUsername)},
		Color:  0x00ff00,
	}}}
}

func ModifiedPayload(username, byUsername string, dataLimit, expire *int64, proxies []string, hasNextPlan bool, resetStrategy string, belongsTo *string) EmbedPayload {
	return EmbedPayload{Embeds: []Embed{{
		Title: ":pencil2: Modified",
		Description: fmt.Sprintf(
			"\n**Username:** %s\n**Traffic Limit:** %s\n**Expire Date:** %s\n**Proxies:** %s\n**Data Limit Reset Strategy:**%s\n**Has Next Plan:**%t",
			username, formatDataLimit(dataLimit), formatExpire(expire), joinProxies(proxies), resetStrategy, hasNextPlan,
		),
		Footer: &Footer{Text: fmt.Sprintf("Belongs To: %s\nBy: %s", belongsToText(belongsTo), byUsername)},
		Color:  0x00ffff,
	}}}
}

func DeletedPayload(username, byUsername string, belongsTo *string) EmbedPayload {
	return EmbedPayload{Embeds: []Embed{{
		Title:       ":wastebasket: Deleted",
		Description: "**Username: **" + username,
		Footer:      &Footer{Text: fmt.Sprintf("Belongs To: %s\nBy: %s", belongsToText(belongsTo), byUsername)},
		Color:       0xff0000,
	}}}
}

func UsageResetPayload(username, byUsername string, belongsTo *string) EmbedPayload {
	return EmbedPayload{Embeds: []Embed{{
		Title:       ":repeat: Reset",
		Description: "**Username:** " + username,
		Footer:      &Footer{Text: fmt.Sprintf("Belongs To: %s\nBy: %s", belongsToText(belongsTo), byUsername)},
		Color:       0x00ffff,
	}}}
}

func DataResetByNextPayload(username string, dataLimit, expire *int64) EmbedPayload {
	return EmbedPayload{Embeds: []Embed{{
		Title: ":repeat: AutoReset",
		Description: fmt.Sprintf(
			"\n**Username:** %s\n**Traffic Limit:** %s\n**Expire Date:** %s",
			username, formatDataLimit(dataLimit), formatExpire(expire),
		),
		Color: 0x00ffff,
	}}}
}

func SubscriptionRevokedPayload(username, byUsername string, belongsTo *string) EmbedPayload {
	return EmbedPayload{Embeds: []Embed{{
		Title:       ":repeat: Revoked",
		Description: "**Username:** " + username,
		Footer:      &Footer{Text: fmt.Sprintf("Belongs To: %s\nBy: %s", belongsToText(belongsTo), byUsername)},
		Color:       0xff0000,
	}}}
}

// LoginPayload deliberately has no password parameter - see
// telegram.LoginMessage's comment; the same security decision applies to
// both integrations.
func LoginPayload(username, clientIP, status string) EmbedPayload {
	return EmbedPayload{Embeds: []Embed{{
		Title:       ":repeat: Login",
		Description: fmt.Sprintf("\n**Username:** %s\n**Client ip**: %s", username, clientIP),
		Footer:      &Footer{Text: "login status: " + status},
		Color:       0xff0000,
	}}}
}
