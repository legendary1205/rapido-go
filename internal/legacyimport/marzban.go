package legacyimport

import (
	"fmt"
	"time"
)

// marzbanProxyTypeMap normalizes the old schema's CamelCase proxy type enum
// ('VMess','VLESS','Trojan','Shadowsocks') to this codebase's lowercase
// one - the two schemas otherwise match column-for-column on this table,
// but sqlc/Postgres's own proxies.type CHECK constraint requires the
// lowercase form.
var marzbanProxyTypeMap = map[string]string{
	"VMess":       "vmess",
	"VLESS":       "vless",
	"Trojan":      "trojan",
	"Shadowsocks": "shadowsocks",
}

// FromMarzbanMySQLDump interprets a parsed legacy Marzban/Rapido mysqldump
// into the common ImportedData shape. Column names match the new schema
// almost exactly (both are descended from the same Marzban lineage - see
// the Phase 8.2 plan) - the real work here is type conversion (MySQL
// datetime strings -> time.Time, tinyint(1) -> bool) and the few places
// that genuinely differ (the proxy type enum's casing, the four newer
// tables - integration_settings/host_metrics/tickets/ticket_messages -
// this source schema predates and simply won't have any rows for).
func FromMarzbanMySQLDump(dump *MySQLDump) ImportedData {
	var out ImportedData

	// Marzban keeps its signing key as the single row of the `jwt` table,
	// stored as a 64-char hex string - the same shape rapido-go's
	// jwt_secrets uses, so it transfers verbatim.
	if rows, ok := dump.Row("jwt"); ok {
		for _, r := range rows {
			if v := optStringPtr(r, "secret_key"); v != nil && *v != "" {
				out.SubscriptionSecret = *v
				break
			}
		}
	}

	adminsByID := map[int64]struct{}{}
	if rows, ok := dump.Row("admins"); ok {
		for _, r := range rows {
			id := reqInt64(r, "id")
			adminsByID[id] = struct{}{}
			out.Admins = append(out.Admins, Admin{
				SourceID:        id,
				Username:        reqString(r, "username"),
				HashedPassword:  reqString(r, "hashed_password"),
				CreatedAt:       optTime(r, "created_at", &out.Warnings, "admins", id),
				IsSudo:          reqInt64(r, "is_sudo") != 0,
				PasswordResetAt: optTimePtr(r, "password_reset_at"),
				TelegramID:      optInt64Ptr(r, "telegram_id"),
				DiscordWebhook:  optStringPtr(r, "discord_webhook"),
				UsersUsage:      reqInt64(r, "users_usage"),
			})
		}
	}

	// inbound tags this dump actually declares, so hosts/exclusions
	// referencing an inbound tag that (for whatever reason) has no
	// matching inbounds row can still be imported with a warning instead
	// of silently dropped or failing the whole import.
	knownInboundTags := map[string]bool{}
	if rows, ok := dump.Row("inbounds"); ok {
		for _, r := range rows {
			tag := reqString(r, "tag")
			knownInboundTags[tag] = true
			out.Inbounds = append(out.Inbounds, Inbound{Tag: tag})
		}
	}

	excludedByProxyID := map[int64][]string{}
	if rows, ok := dump.Row("exclude_inbounds_association"); ok {
		for _, r := range rows {
			pid, ok := optInt64(r, "proxy_id")
			tag := reqString(r, "inbound_tag")
			if !ok {
				continue
			}
			if !knownInboundTags[tag] {
				out.Warnings = append(out.Warnings, fmt.Sprintf(
					"exclude_inbounds_association: proxy %d excludes unknown inbound tag %q, kept anyway", pid, tag))
			}
			excludedByProxyID[pid] = append(excludedByProxyID[pid], tag)
		}
	}

	proxiesByUserID := map[int64][]Proxy{}
	if rows, ok := dump.Row("proxies"); ok {
		for _, r := range rows {
			uid, ok := optInt64(r, "user_id")
			if !ok {
				continue
			}
			oldType := reqString(r, "type")
			newType, known := marzbanProxyTypeMap[oldType]
			if !known {
				pid := reqInt64(r, "id")
				out.Warnings = append(out.Warnings, fmt.Sprintf(
					"proxies: row %d has unrecognized type %q, skipped", pid, oldType))
				continue
			}
			pid := reqInt64(r, "id")
			settings, _ := r["settings"].(string)
			proxiesByUserID[uid] = append(proxiesByUserID[uid], Proxy{
				Type:             newType,
				Settings:         []byte(settings),
				ExcludedInbounds: excludedByProxyID[pid],
			})
		}
	}

	if rows, ok := dump.Row("users"); ok {
		for _, r := range rows {
			id := reqInt64(r, "id")
			var adminID *int64
			if aid, ok := optInt64(r, "admin_id"); ok {
				if _, exists := adminsByID[aid]; exists {
					adminID = &aid
				} else {
					out.Warnings = append(out.Warnings, fmt.Sprintf(
						"users: %q references admin_id %d which doesn't exist in this dump, imported with no owning admin", reqString(r, "username"), aid))
				}
			}
			out.Users = append(out.Users, User{
				SourceID:               id,
				SourceAdminID:          adminID,
				Username:               reqString(r, "username"),
				Status:                 reqString(r, "status"),
				UsedTraffic:            optInt64Or(r, "used_traffic", 0),
				DataLimit:              optInt64Ptr(r, "data_limit"),
				DataLimitResetStrategy: reqString(r, "data_limit_reset_strategy"),
				Expire:                 optInt32Ptr(r, "expire"),
				CreatedAt:              optTime(r, "created_at", &out.Warnings, "users", id),
				Note:                   optStringPtr(r, "note"),
				SubRevokedAt:           optTimePtr(r, "sub_revoked_at"),
				SubUpdatedAt:           optTimePtr(r, "sub_updated_at"),
				SubLastUserAgent:       optStringPtr(r, "sub_last_user_agent"),
				OnlineAt:               optTimePtr(r, "online_at"),
				EditAt:                 optTimePtr(r, "edit_at"),
				OnHoldTimeout:          optTimePtr(r, "on_hold_timeout"),
				OnHoldExpireDuration:   optInt64Ptr(r, "on_hold_expire_duration"),
				AutoDeleteInDays:       optInt32Ptr(r, "auto_delete_in_days"),
				LastStatusChange:       optTimePtr(r, "last_status_change"),
				Proxies:                proxiesByUserID[id],
			})
		}
	}

	if rows, ok := dump.Row("hosts"); ok {
		for _, r := range rows {
			tag := reqString(r, "inbound_tag")
			if !knownInboundTags[tag] {
				out.Warnings = append(out.Warnings, fmt.Sprintf(
					"hosts: %q targets unknown inbound tag %q, kept anyway", reqString(r, "remark"), tag))
			}
			out.Hosts = append(out.Hosts, Host{
				Remark:          reqString(r, "remark"),
				Address:         reqString(r, "address"),
				Port:            optInt32Ptr(r, "port"),
				InboundTag:      tag,
				SNI:             optStringPtr(r, "sni"),
				HostHeader:      optStringPtr(r, "host"),
				Security:        reqString(r, "security"),
				ALPN:            reqString(r, "alpn"),
				Fingerprint:     reqString(r, "fingerprint"),
				AllowInsecure:   optBoolPtr(r, "allowinsecure"),
				IsDisabled:      optBoolPtr(r, "is_disabled"),
				Path:            optStringPtr(r, "path"),
				MuxEnable:       optInt64Or(r, "mux_enable", 0) != 0,
				FragmentSetting: optStringPtr(r, "fragment_setting"),
				RandomUserAgent: optInt64Or(r, "random_user_agent", 0) != 0,
				NoiseSetting:    optStringPtr(r, "noise_setting"),
				UseSNIAsHost:    optInt64Or(r, "use_sni_as_host", 0) != 0,
			})
		}
	}
	if len(out.Inbounds) > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"inbounds: %d imported with placeholder protocol/network/security - this legacy schema never stored those at the database level (only in the live Xray config file, which this import doesn't have). Configure each one's real protocol from the Core Config page before any node can use it.", len(out.Inbounds)))
	}

	templateInboundTags := map[int64][]string{}
	if rows, ok := dump.Row("template_inbounds_association"); ok {
		for _, r := range rows {
			tid, ok := optInt64(r, "user_template_id")
			if !ok {
				continue
			}
			templateInboundTags[tid] = append(templateInboundTags[tid], reqString(r, "inbound_tag"))
		}
	}
	if rows, ok := dump.Row("user_templates"); ok {
		for _, r := range rows {
			id := reqInt64(r, "id")
			out.UserTemplates = append(out.UserTemplates, UserTemplate{
				Name:           reqString(r, "name"),
				DataLimit:      optInt64Ptr(r, "data_limit"),
				ExpireDuration: optInt64Ptr(r, "expire_duration"),
				UsernamePrefix: optStringPtr(r, "username_prefix"),
				UsernameSuffix: optStringPtr(r, "username_suffix"),
				InboundTags:    templateInboundTags[id],
			})
		}
	}

	if rows, ok := dump.Row("next_plans"); ok {
		for _, r := range rows {
			uid, ok := optInt64(r, "user_id")
			if !ok {
				continue
			}
			out.NextPlans = append(out.NextPlans, NextPlan{
				SourceUserID:        uid,
				DataLimit:           reqInt64(r, "data_limit"),
				Expire:              optInt32Ptr(r, "expire"),
				AddRemainingTraffic: optInt64Or(r, "add_remaining_traffic", 0) != 0,
				FireOnEither:        optInt64Or(r, "fire_on_either", 0) != 0,
			})
		}
	}

	return out
}

// --- row value helpers -----------------------------------------------
//
// ParseMySQLDump's row values are always nil, string, int64, or float64
// (see MySQLTable's doc comment) - these helpers convert that into what
// ImportedData actually needs, tolerating a missing/NULL value wherever
// the source column is itself nullable rather than panicking, since a
// real legacy dump is exactly the kind of input that can have surprises.

func reqString(r map[string]any, col string) string {
	s, _ := r[col].(string)
	return s
}

func reqInt64(r map[string]any, col string) int64 {
	v, _ := optInt64(r, col)
	return v
}

func optInt64(r map[string]any, col string) (int64, bool) {
	switch v := r[col].(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

func optInt64Or(r map[string]any, col string, fallback int64) int64 {
	if v, ok := optInt64(r, col); ok {
		return v
	}
	return fallback
}

func optInt64Ptr(r map[string]any, col string) *int64 {
	if v, ok := optInt64(r, col); ok {
		return &v
	}
	return nil
}

func optInt32Ptr(r map[string]any, col string) *int32 {
	if v, ok := optInt64(r, col); ok {
		v32 := int32(v)
		return &v32
	}
	return nil
}

func optStringPtr(r map[string]any, col string) *string {
	if s, ok := r[col].(string); ok {
		return &s
	}
	return nil
}

func optBoolPtr(r map[string]any, col string) *bool {
	if v, ok := optInt64(r, col); ok {
		b := v != 0
		return &b
	}
	return nil
}

const mysqlDatetimeLayout = "2006-01-02 15:04:05"

func optTimePtr(r map[string]any, col string) *time.Time {
	s, ok := r[col].(string)
	if !ok || s == "" {
		return nil
	}
	t, err := time.Parse(mysqlDatetimeLayout, s)
	if err != nil {
		return nil
	}
	return &t
}

// optTime is for columns this codebase treats as required (created_at) -
// falls back to "now" with a warning rather than importing a zero-value
// timestamp if the source row is somehow missing it.
func optTime(r map[string]any, col string, warnings *[]string, table string, id int64) time.Time {
	if t := optTimePtr(r, col); t != nil {
		return *t
	}
	*warnings = append(*warnings, fmt.Sprintf("%s: row %d missing %s, defaulted to the import time", table, id, col))
	return time.Now().UTC()
}
