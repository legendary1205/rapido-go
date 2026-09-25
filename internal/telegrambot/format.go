package telegrambot

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const gib = int64(1) << 30

// maxMessageRunes is a little under Telegram's 4096 limit: the count Telegram
// applies is after entity parsing, so leaving slack for the cut marker is cheap.
const maxMessageRunes = 4000

func formatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	switch {
	case v >= 100:
		return fmt.Sprintf("%.0f %s", v, units[i])
	case v >= 10:
		return fmt.Sprintf("%.1f %s", v, units[i])
	default:
		return fmt.Sprintf("%.2f %s", v, units[i])
	}
}

// usagePercent is used/limit clamped to 0..100; 0 when there is no limit.
func usagePercent(used, limit int64) int {
	if limit <= 0 || used <= 0 {
		return 0
	}
	p := int(math.Round(float64(used) / float64(limit) * 100))
	if p > 100 {
		return 100
	}
	if p == 0 {
		return 1 // some usage must never look like none
	}
	return p
}

func progressBar(used, limit int64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := int(math.Round(float64(usagePercent(used, limit)) / 100 * float64(width)))
	if used > 0 && limit > 0 && filled == 0 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// daysLeft rounds up so a subscription ending later today still reads as one
// day left rather than zero. Negative once expired.
func daysLeft(expire int64, now time.Time) int {
	secs := expire - now.Unix()
	if secs >= 0 {
		return int(math.Ceil(float64(secs) / 86400))
	}
	return -int(math.Ceil(float64(-secs) / 86400))
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// truncateBytes cuts on a rune boundary, for values that must fit a byte budget
// such as callback data.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// capMessage keeps a message under Telegram's size limit. It cuts at a line
// boundary so markup is never split: every tag the console writes opens and
// closes on the same line.
func capMessage(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	cut := string(r[:max-1])
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n…"
}

// normalizeDigits maps Persian and Arabic-Indic digits (and the Persian decimal
// separator) to ASCII, since admins type numbers on a Persian keyboard.
func normalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case r == '٫':
			b.WriteRune('.')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var numberInput = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(?:gb|gib|g|days?|d)?$`)

// parseNumber reads "10", "2.5", "10 GB" or "30d", in any digit script.
func parseNumber(s string) (float64, bool) {
	m := numberInput.FindStringSubmatch(strings.ToLower(strings.TrimSpace(normalizeDigits(s))))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}

const (
	maxDataGB = 100000
	maxDays   = 3650
)

// parseGB parses a data amount in GB. zeroOK admits 0, meaning unlimited.
func parseGB(s string, zeroOK bool) (int64, bool) {
	v, ok := parseNumber(s)
	if !ok || v > maxDataGB || (v == 0 && !zeroOK) {
		return 0, false
	}
	return int64(math.Round(v * float64(gib))), true
}

// parseDays parses a whole number of days. zeroOK admits 0, meaning unlimited.
func parseDays(s string, zeroOK bool) (int, bool) {
	v, ok := parseNumber(s)
	if !ok || v != math.Trunc(v) || v > maxDays || (v == 0 && !zeroOK) {
		return 0, false
	}
	return int(v), true
}

var usernameChars = regexp.MustCompile(`^[a-zA-Z0-9_@.-]+$`)

// validUsername applies the panel's own username rule (the leading run of word
// characters must be 3-32 long) so the wizard rejects bad input before any
// request is made; the API remains the authority and its refusal is still shown.
func validUsername(s string) bool {
	if !usernameChars.MatchString(s) {
		return false
	}
	run := 0
	for _, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			run++
		} else {
			break
		}
	}
	return run >= 3 && run <= 32 && len(s) <= 34
}
