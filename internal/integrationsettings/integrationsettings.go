// Package integrationsettings merges the single-row integration_settings
// DB override with the process's env-var defaults, mirroring
// app/utils/settings.py's per-field "DB value if set, else env value"
// fallback (a NULL/empty column there means "not set", identical here).
package integrationsettings

import (
	"encoding/json"
	"strings"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// Values holds every integration_settings column already resolved to a
// plain Go value - no pgtype, no NULL, either the DB override or the env
// fallback. Used both as the env-defaults input to Resolve and as its
// merged output.
type Values struct {
	KirbotSecret, KirbotURL, KirbotLicense string

	TelegramAPIToken         string
	TelegramAdminIDs         []int64
	TelegramProxyURL         string
	TelegramLoggerChannelID  int64
	TelegramLoggerTopicID    int64
	TelegramDefaultVlessFlow string

	WebhookAddresses  []string
	WebhookSecret     string
	DiscordWebhookURL string
}

// Resolve merges a raw integration_settings row over env, field by field:
// "" / nil / empty-slice on the DB side all mean "not set, use env" -
// matching settings.py's `value not in (None, "", [])` check exactly.
func Resolve(row generated.IntegrationSetting, env Values) Values {
	out := env

	if row.KirbotSecret.Valid && row.KirbotSecret.String != "" {
		out.KirbotSecret = row.KirbotSecret.String
	}
	if row.KirbotUrl.Valid && row.KirbotUrl.String != "" {
		out.KirbotURL = row.KirbotUrl.String
	}
	if row.KirbotLicense.Valid && row.KirbotLicense.String != "" {
		out.KirbotLicense = row.KirbotLicense.String
	}
	if row.TelegramApiToken.Valid && row.TelegramApiToken.String != "" {
		out.TelegramAPIToken = row.TelegramApiToken.String
	}
	if row.TelegramProxyUrl.Valid && row.TelegramProxyUrl.String != "" {
		out.TelegramProxyURL = row.TelegramProxyUrl.String
	}
	if row.TelegramLoggerChannelID.Valid && row.TelegramLoggerChannelID.Int64 != 0 {
		out.TelegramLoggerChannelID = row.TelegramLoggerChannelID.Int64
	}
	if row.TelegramLoggerTopicID.Valid && row.TelegramLoggerTopicID.Int64 != 0 {
		out.TelegramLoggerTopicID = row.TelegramLoggerTopicID.Int64
	}
	if row.TelegramDefaultVlessFlow.Valid && row.TelegramDefaultVlessFlow.String != "" {
		out.TelegramDefaultVlessFlow = row.TelegramDefaultVlessFlow.String
	}
	if row.WebhookSecret.Valid && row.WebhookSecret.String != "" {
		out.WebhookSecret = row.WebhookSecret.String
	}
	if row.DiscordWebhookUrl.Valid && row.DiscordWebhookUrl.String != "" {
		out.DiscordWebhookURL = row.DiscordWebhookUrl.String
	}
	if ids := decodeInt64s(row.TelegramAdminIds); len(ids) > 0 {
		out.TelegramAdminIDs = ids
	}
	if addrs := decodeStrings(row.WebhookAddresses); len(addrs) > 0 {
		out.WebhookAddresses = addrs
	}

	return out
}

func decodeInt64s(raw []byte) []int64 {
	if len(raw) == 0 {
		return nil
	}
	var v []int64
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func decodeStrings(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var v []string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// Mask reproduces routers/settings.py's _mask: empty stays empty, a value
// of 4 characters or fewer becomes all stars, otherwise stars plus the last
// 4 characters. Callers rendering a masked value as JSON turn "" into a nil
// *string, matching Python's `_mask` returning None for an empty value.
func Mask(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return strings.Repeat("*", len(value))
	}
	return strings.Repeat("*", len(value)-4) + value[len(value)-4:]
}
