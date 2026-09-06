package subscription

import (
	"fmt"
	"math"
	"regexp"
	"time"
)

// Variables mirrors app/subscription/share.py's setup_format_variables: the
// {VAR}-style placeholders usable in a host's remark/address/path template.
// SERVER_IPV6 and JALALI_EXPIRE_DATE (Persian calendar) from the Python
// original are deferred - neither is load-bearing for the wire formats
// themselves, just optional remark cosmetics.
type Variables map[string]string

var placeholderPattern = regexp.MustCompile(`\{[A-Z_]+\}`)

// Format substitutes every {VAR} in s using v, leaving "<missing>" for any
// placeholder v doesn't define - matches the Python defaultdict(lambda:
// "<missing>", ...) behavior exactly, so a typo'd placeholder in an admin's
// remark template renders visibly wrong rather than raising an error.
func (v Variables) Format(s string) string {
	return placeholderPattern.ReplaceAllStringFunc(s, func(token string) string {
		key := token[1 : len(token)-1]
		if val, ok := v[key]; ok {
			return val
		}
		return "<missing>"
	})
}

// UserInfo is the subset of a user's state needed to compute the
// placeholder values - deliberately narrow rather than depending on the
// full generated.User row, so this package doesn't need a DB import.
type UserInfo struct {
	Username       string
	Status         string // active/disabled/limited/expired/on_hold
	UsedTraffic    int64
	DataLimit      *int64
	Expire         *int64 // unix seconds, nil = unlimited
	OnHold         bool
	OnHoldDuration *int64 // seconds, only meaningful when OnHold
}

// BuildVariables computes every placeholder for one user - call once per
// subscription request, then add PROTOCOL/TRANSPORT/ACTIVE_USERS per host
// inside the generation loop (those three vary per inbound, not per user).
func BuildVariables(u UserInfo, serverIP string) Variables {
	v := Variables{
		"SERVER_IP":  serverIP,
		"USERNAME":   u.Username,
		"DATA_USAGE": ReadableSize(u.UsedTraffic),
	}

	if u.DataLimit != nil && *u.DataLimit > 0 {
		v["DATA_LIMIT"] = ReadableSize(*u.DataLimit)
		left := *u.DataLimit - u.UsedTraffic
		if left < 0 {
			left = 0
		}
		v["DATA_LEFT"] = ReadableSize(left)
		v["USAGE_PERCENTAGE"] = fmt.Sprintf("%.2f", math.Min(100, float64(u.UsedTraffic)*100/float64(*u.DataLimit)))
	} else {
		v["DATA_LIMIT"] = "∞"
		v["DATA_LEFT"] = "∞"
		v["USAGE_PERCENTAGE"] = "∞"
	}

	switch {
	case u.OnHold:
		v["DAYS_LEFT"] = daysFromSeconds(u.OnHoldDuration)
		v["EXPIRE_DATE"] = "-"
		v["TIME_LEFT"] = "∞"
	case u.Expire == nil || *u.Expire == 0:
		v["DAYS_LEFT"] = "∞"
		v["EXPIRE_DATE"] = "∞"
		v["TIME_LEFT"] = "∞"
	default:
		expireAt := time.Unix(*u.Expire, 0).UTC()
		remaining := time.Until(expireAt)
		if remaining < 0 {
			v["DAYS_LEFT"] = "0"
			v["TIME_LEFT"] = "0"
		} else {
			v["DAYS_LEFT"] = fmt.Sprintf("%d", int(remaining.Hours()/24))
			v["TIME_LEFT"] = formatTimeLeft(remaining)
		}
		v["EXPIRE_DATE"] = expireAt.Format("2006-01-02")
	}

	v["STATUS_EMOJI"] = statusEmoji(u.Status)
	v["STATUS_TEXT"] = u.Status
	return v
}

func statusEmoji(status string) string {
	switch status {
	case "active":
		return "✅"
	case "on_hold":
		return "🔌"
	case "limited":
		return "🪫"
	case "expired":
		return "⌛️"
	case "disabled":
		return "❌"
	default:
		return "❓"
	}
}

func daysFromSeconds(seconds *int64) string {
	if seconds == nil || *seconds == 0 {
		return "∞"
	}
	return fmt.Sprintf("%d", *seconds/86400)
}

// formatTimeLeft mirrors the Python format_time_left helper's "2m 5d 3h"
// style human-readable duration, months/days/hours only (no minutes) -
// close enough for a remark string, not a precise countdown.
func formatTimeLeft(d time.Duration) string {
	totalHours := int(d.Hours())
	months := totalHours / (24 * 30)
	totalHours -= months * 24 * 30
	days := totalHours / 24
	hours := totalHours - days*24

	out := ""
	if months > 0 {
		out += fmt.Sprintf("%dm ", months)
	}
	if days > 0 || months > 0 {
		out += fmt.Sprintf("%dd ", days)
	}
	out += fmt.Sprintf("%dh", hours)
	return out
}

// ReadableSize mirrors app/utils/system.py's readable_size: binary units,
// two decimals.
func ReadableSize(bytes int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"}
	size := float64(bytes)
	for _, unit := range units {
		if size < 1024 {
			return fmt.Sprintf("%.2f %s", size, unit)
		}
		size /= 1024
	}
	return fmt.Sprintf("%.2f YB", size)
}
