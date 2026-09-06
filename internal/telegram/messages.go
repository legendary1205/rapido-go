package telegram

import (
	"fmt"
	"html"
	"time"

	"github.com/legendary1205/rapido-go/internal/subscription"
)

// belongsToText mirrors app/telegram/handlers/report.py's
// `admin.username if admin else None` fed straight into a Python
// str.format() call - when there's no owning admin, Python's formatter
// stringifies None as the literal text "None", which is what every existing
// Telegram admin has actually been seeing. Preserved here rather than
// "fixed" to a blank/dash, since this is cosmetic and changing it would
// make a real admin's message history inconsistent with what's shown from
// here on.
func belongsToText(belongsTo *string) string {
	if belongsTo == nil {
		return "None"
	}
	return html.EscapeString(*belongsTo)
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
	// Local server time, matching Python's naive datetime.fromtimestamp
	// (not forced to UTC) - admins are used to seeing local time here.
	return time.Unix(*expire, 0).Format("15:04:05 2006-01-02")
}

func joinProxies(proxies []string) string {
	out := ""
	for i, p := range proxies {
		if i > 0 {
			out += ", "
		}
		out += html.EscapeString(p)
	}
	return out
}

func NewUserMessage(username, byUsername string, dataLimit, expire *int64, proxies []string, hasNextPlan bool, resetStrategy string, belongsTo *string) string {
	return fmt.Sprintf(
		"🆕 <b>#Created</b>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Username :</b> <code>%s</code>\n"+
			"<b>Traffic Limit :</b> <code>%s</code>\n"+
			"<b>Expire Date :</b> <code>%s</code>\n"+
			"<b>Proxies :</b> <code>%s</code>\n"+
			"<b>Data Limit Reset Strategy :</b> <code>%s</code>\n"+
			"<b>Has Next Plan :</b> <code>%s</code>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Belongs To :</b> <code>%s</code>\n"+
			"<b>By :</b> <b>#%s</b>",
		html.EscapeString(username), formatDataLimit(dataLimit), formatExpire(expire),
		joinProxies(proxies), html.EscapeString(resetStrategy), boolText(hasNextPlan),
		belongsToText(belongsTo), html.EscapeString(byUsername),
	)
}

func ModifiedMessage(username, byUsername string, dataLimit, expire *int64, proxies []string, hasNextPlan bool, resetStrategy string, belongsTo *string) string {
	return fmt.Sprintf(
		"✏️ <b>#Modified</b>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Username :</b> <code>%s</code>\n"+
			"<b>Traffic Limit :</b> <code>%s</code>\n"+
			"<b>Expire Date :</b> <code>%s</code>\n"+
			"<b>Protocols :</b> <code>%s</code>\n"+
			"<b>Data Limit Reset Strategy :</b> <code>%s</code>\n"+
			"<b>Has Next Plan :</b> <code>%s</code>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Belongs To :</b> <code>%s</code>\n"+
			"<b>By :</b> <b>#%s</b>",
		html.EscapeString(username), formatDataLimit(dataLimit), formatExpire(expire),
		joinProxies(proxies), html.EscapeString(resetStrategy), boolText(hasNextPlan),
		belongsToText(belongsTo), html.EscapeString(byUsername),
	)
}

func DeletedMessage(username, byUsername string, belongsTo *string) string {
	return fmt.Sprintf(
		"🗑 <b>#Deleted</b>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Username</b> : <code>%s</code>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Belongs To :</b> <code>%s</code>\n"+
			"<b>By</b> : <b>#%s</b>",
		html.EscapeString(username), belongsToText(belongsTo), html.EscapeString(byUsername),
	)
}

// statusLabels mirrors app/telegram/handlers/report.py's `_status` map.
// Fixed vs. Python: that map has no "on_hold" entry, so a manual PUT
// /api/user setting status=on_hold raises a KeyError there that the outer
// bare `except` swallows - report.status_change silently sends nothing.
// on_hold is a reachable status_change value in both systems (an admin can
// PUT a disabled user straight to on_hold), so it needs a real entry, not
// just the automatic reviewjob on_hold->active transition.
var statusLabels = map[string]string{
	"active":   "✅ <b>#Activated</b>",
	"disabled": "❌ <b>#Disabled</b>",
	"limited":  "🪫 <b>#Limited</b>",
	"expired":  "🕔 <b>#Expired</b>",
	"on_hold":  "🔌 <b>#OnHold</b>",
}

func StatusChangeMessage(username, status string, belongsTo *string) string {
	label, ok := statusLabels[status]
	if !ok {
		label = "<b>#" + html.EscapeString(status) + "</b>"
	}
	return fmt.Sprintf(
		"%s\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Username</b> : <code>%s</code>\n"+
			"<b>Belongs To :</b> <code>%s</code>",
		label, html.EscapeString(username), belongsToText(belongsTo),
	)
}

func UsageResetMessage(username, byUsername string, belongsTo *string) string {
	return fmt.Sprintf(
		"🔁 <b>#Reset</b>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Username</b> : <code>%s</code>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Belongs To :</b> <code>%s</code>\n"+
			"<b>By</b> : <b>#%s</b>",
		html.EscapeString(username), belongsToText(belongsTo), html.EscapeString(byUsername),
	)
}

func DataResetByNextMessage(username string, dataLimit, expire *int64) string {
	return fmt.Sprintf(
		"🔁 <b>#AutoReset</b>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Username :</b> <code>%s</code>\n"+
			"<b>Traffic Limit :</b> <code>%s</code>\n"+
			"<b>Expire Date :</b> <code>%s</code>\n"+
			"➖➖➖➖➖➖➖➖➖",
		html.EscapeString(username), formatDataLimit(dataLimit), formatExpire(expire),
	)
}

func SubscriptionRevokedMessage(username, byUsername string, belongsTo *string) string {
	return fmt.Sprintf(
		"🔁 <b>#Revoked</b>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Username</b> : <code>%s</code>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Belongs To :</b> <code>%s</code>\n"+
			"<b>By</b> : <b>#%s</b>",
		html.EscapeString(username), belongsToText(belongsTo), html.EscapeString(byUsername),
	)
}

// LoginMessage deliberately has no password parameter at all - the current
// Python system's report_login sends the admin's plaintext attempted
// password to this exact message (permanently, into Telegram's chat
// history); the user explicitly decided this rewrite must not replicate
// that, so only username/client_ip/status are reported.
func LoginMessage(username, clientIP, status string) string {
	return fmt.Sprintf(
		"🔐 <b>#Login</b>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>Username</b> : <code>%s</code>\n"+
			"<b>Client ip</b> : <code>%s</code>\n"+
			"➖➖➖➖➖➖➖➖➖\n"+
			"<b>login status</b> : <code>%s</code>",
		html.EscapeString(username), html.EscapeString(clientIP), html.EscapeString(status),
	)
}

func boolText(b bool) string {
	if b {
		return "True"
	}
	return "False"
}
