package legacyimport

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const pasarGuardTestSecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// pasarGuardFixture is a small synthetic dump shaped like a real PasarGuard
// 5.x pg_dump (TimescaleDB catalog block included) - never a real panel's
// export, see TestFromPasarGuardRealSample for that.
func pasarGuardFixture() string {
	var b strings.Builder
	b.WriteString("--\n-- PostgreSQL database dump\n--\n\nSET statement_timeout = 0;\n")
	b.WriteString("CREATE EXTENSION IF NOT EXISTS timescaledb WITH SCHEMA public;\n")
	for _, t := range []string{"admin_roles", "admins", "alembic_version", "core_configs", "groups", "hosts",
		"inbounds", "inbounds_groups_association", "jwt", "next_plans", "settings", "user_hwids",
		"user_subscription_updates", "users", "users_groups_association", "node_user_usages"} {
		b.WriteString("CREATE TABLE public." + t + " (\n    id bigint\n);\n")
	}
	b.WriteString(copyBlock("_timescaledb_catalog.metadata", []string{"key", "value", "include_in_telemetry"}, []string{"exported_uuid", "abc", "t"}))
	b.WriteString(copyBlock("public.admin_roles", []string{"id", "name", "is_owner"},
		[]string{"1", "owner", "t"}, []string{"2", "administrator", "f"}, []string{"3", "operator", "f"}, []string{"4", "auditor", "f"}))
	b.WriteString(copyBlock("public.admins",
		[]string{"id", "username", "hashed_password", "created_at", "password_reset_at", "telegram_id", "discord_webhook", "used_traffic", "sub_domain", "role_id", "status", "data_limit"},
		[]string{"1", "boss", "$2b$12$bosshash", "2026-06-28 18:59:53.044686+00", `\N`, "45312485", `\N`, "1000", `\N`, "1", "active", `\N`},
		[]string{"2", "seller", "$2b$12$sellerhash", "2026-08-08 13:33:00+03:30", `\N`, `\N`, "", "50", `\N`, "3", "active", `\N`},
		[]string{"3", "watcher", "$2b$12$watcherhash", "2026-08-09 00:00:00+00", `\N`, `\N`, `\N`, "0", "sub.watcher.example", "4", "disabled", "1000"},
	))
	b.WriteString(copyBlock("public.core_configs", []string{"id", "created_at", "name", "config", "exclude_inbound_tags", "fallbacks_inbound_tags", "type"},
		[]string{"1", "2026-06-28 18:46:11+00", "Default",
			`{"inbounds": [{"tag": "VLESS WS", "protocol": "vless"}, {"tag": "Trojan TCP", "protocol": "trojan"}, {"tag": "Hy", "protocol": "hysteria"}], "outbounds": []}`,
			`\N`, `\N`, "xray"}))
	b.WriteString(copyBlock("public.groups", []string{"id", "name", "is_disabled"},
		[]string{"1", "main", "f"}, []string{"2", "extra", "f"}, []string{"3", "frozen", "t"}))
	b.WriteString(copyBlock("public.hosts", []string{"id", "remark", "address", "port", "inbound_tag", "is_disabled"},
		[]string{"1", "Live host", "cdn.example.com", "443", "VLESS WS", "f"},
		[]string{"2", "Old host", "old.example.com", "8443", "VLESS WS", "t"}))
	b.WriteString(copyBlock("public.inbounds", []string{"id", "tag"},
		[]string{"10", "VLESS WS"}, []string{"11", "Trojan TCP"}, []string{"12", "Hy"}, []string{"13", "Gone"}))
	b.WriteString(copyBlock("public.inbounds_groups_association", []string{"inbound_id", "group_id"},
		[]string{"10", "1"}, []string{"11", "2"}, []string{"12", "2"}, []string{"13", "2"}, []string{"11", "3"}))
	b.WriteString(copyBlock("public.jwt", []string{"id", "secret_key"}, []string{"1", pasarGuardTestSecret}))
	b.WriteString(copyBlock("public.next_plans", []string{"id", "user_id", "data_limit", "expire", "add_remaining_traffic", "user_template_id"},
		[]string{"1", "7", "1073741824", "2592000", "t", `\N`}))
	b.WriteString(copyBlock("public.node_user_usages", []string{"id", "created_at", "user_id", "node_id", "used_traffic"},
		[]string{"1", "2026-09-01 00:00:00+00", "7", "1", "100"}))
	b.WriteString(copyBlock("public.settings", []string{"id", "subscription", "general"},
		[]string{"1", `{"url_prefix": "https://sub.example.com:8443/", "rules": []}`, `{"default_method": "aes-256-gcm"}`}))
	b.WriteString(copyBlock("public.user_hwids", []string{"id", "user_id", "hwid", "device_os", "os_version", "device_model", "created_at", "last_used_at"},
		[]string{"1", "7", "HW1", `\N`, `\N`, `\N`, "2026-09-01 00:00:00+00", "2026-09-01 00:00:00+00"}))
	b.WriteString(copyBlock("public.user_subscription_updates", []string{"id", "user_id", "created_at", "user_agent", "ip", "hwid"},
		[]string{"1", "7", "2026-10-01 04:12:29.568091+00", "V2Box 10.1.8/iOS 17.5.1", "1.2.3.4", `\N`},
		[]string{"2", "7", "2026-09-30 10:00:00+00", "OldApp/1.0", "1.2.3.4", `\N`}))
	b.WriteString(copyBlock("public.users",
		[]string{"id", "username", "status", "used_traffic", "data_limit", "created_at", "admin_id", "data_limit_reset_strategy", "sub_revoked_at", "note", "online_at", "edit_at", "on_hold_timeout", "on_hold_expire_duration", "auto_delete_in_days", "last_status_change", "expire", "proxy_settings", "hwid_limit"},
		[]string{"7", "Ali.shvip", "active", "47231285", "4294967296", "2026-09-29 16:11:08.91145+00", "1", "no_reset", `\N`, `tab\there`, "2026-09-30 06:22:07.435567+00", `\N`, `\N`, `\N`, `\N`, `\N`, "2026-10-26 13:22:52+00",
			`{"vmess": {"id": "201cfd36-1303-4245-ba33-42585670ed53"}, "vless": {"id": "688b1a71-fde2-4819-aa5d-b9d71dfa7780"}, "trojan": {"password": "tp"}, "shadowsocks": {"password": "sp", "method": "chacha20-ietf-poly1305"}, "hysteria": {"auth": "hy"}}`, "2"},
		[]string{"1059", "Parima.Gozarban", "limited", "100", "100", "2026-09-01 00:00:00+00", "1", "month", "2026-09-15 00:00:00+00", "", `\N`, `\N`, `\N`, `\N`, `\N`, "2026-09-20 00:00:00+00", `\N`,
			`{"vless": {"id": "11111111-1111-1111-1111-111111111111", "flow": "xtls-rprx-vision"}}`, `\N`},
		[]string{"1060", "Parima.gozarban", "active", "0", `\N`, "2026-09-02 00:00:00+00", "2", "no_reset", `\N`, `\N`, `\N`, `\N`, `\N`, `\N`, `\N`, `\N`, `\N`,
			`{"vless": {"id": "22222222-2222-2222-2222-222222222222"}, "trojan": {"password": "tp2"}, "shadowsocks": {"password": "sp2"}}`, `\N`},
		[]string{"1061", "nogroup", "active", "0", `\N`, "2026-09-03 00:00:00+00", "1", "no_reset", `\N`, `\N`, `\N`, `\N`, `\N`, `\N`, `\N`, `\N`, `\N`, `{}`, `\N`},
		[]string{"1062", "frozen_only", "on_hold", "0", `\N`, "2026-09-04 00:00:00+00", `\N`, "no_reset", `\N`, `\N`, `\N`, `\N`, "2026-11-01 00:00:00+00", "2592000", "7", `\N`, `\N`, `{"trojan": {"password": "tp3"}}`, `\N`},
	))
	b.WriteString(copyBlock("public.users_groups_association", []string{"user_id", "groups_id"},
		[]string{"7", "1"}, []string{"1059", "1"}, []string{"1060", "1"}, []string{"1060", "2"}, []string{"1062", "3"}))
	b.WriteString("\n--\n-- PostgreSQL database dump complete\n--\n")
	return b.String()
}

func parseFixture(t *testing.T) *PgDump {
	t.Helper()
	d, err := ParsePgDump(strings.NewReader(pasarGuardFixture()), PasarGuardTables)
	if err != nil {
		t.Fatalf("ParsePgDump: %v", err)
	}
	return d
}

func TestIsPasarGuardSchema(t *testing.T) {
	d := parseFixture(t)
	if !IsPasarGuardSchema(d.Created) {
		t.Fatal("the PasarGuard fixture was not recognized")
	}
	native := map[string]bool{"users": true, "admins": true, "core_config": true, "jwt_secrets": true, "goose_db_version": true}
	if IsPasarGuardSchema(native) {
		t.Error("this panel's own schema was taken for PasarGuard")
	}
	if _, ok := d.Tables["node_user_usages"]; ok {
		t.Error("PasarGuardTables kept the usage history table")
	}
}

func TestFromPasarGuard(t *testing.T) {
	data, err := FromPasarGuard(parseFixture(t))
	if err != nil {
		t.Fatalf("FromPasarGuard: %v", err)
	}
	if !data.KeepUserIDs || !data.UsersOnly {
		t.Errorf("KeepUserIDs=%v UsersOnly=%v, want both true", data.KeepUserIDs, data.UsersOnly)
	}
	if data.SubscriptionSecret != pasarGuardTestSecret {
		t.Errorf("secret = %q, want the jwt row's key", data.SubscriptionSecret)
	}
	if len(data.Hosts) != 0 || len(data.Inbounds) != 0 || len(data.UserTemplates) != 0 {
		t.Errorf("a users-only import carried panel config: %d hosts, %d inbounds, %d templates", len(data.Hosts), len(data.Inbounds), len(data.UserTemplates))
	}

	// Admins: the owner role is sudo+owner, operator is a regular admin.
	admins := map[string]Admin{}
	for _, a := range data.Admins {
		admins[a.Username] = a
	}
	if a := admins["boss"]; !a.IsSudo || !a.IsOwner || a.HashedPassword != "$2b$12$bosshash" || a.UsersUsage != 1000 || a.TelegramID == nil || *a.TelegramID != 45312485 {
		t.Errorf("boss = %+v", a)
	}
	if a := admins["seller"]; a.IsSudo || a.IsOwner || a.DiscordWebhook != nil {
		t.Errorf("seller = %+v, want a regular admin with no webhook", a)
	}
	if want := time.Date(2026, 8, 8, 10, 3, 0, 0, time.UTC); !admins["seller"].CreatedAt.Equal(want) {
		t.Errorf("seller created_at = %v, want %v (the +03:30 offset applied)", admins["seller"].CreatedAt, want)
	}

	users := map[string]User{}
	for _, u := range data.Users {
		users[u.Username] = u
	}
	if len(users) != 5 {
		t.Fatalf("users = %d, want 5 (case-different usernames are distinct)", len(users))
	}

	ali := users["Ali.shvip"]
	if ali.SourceID != 7 || ali.Status != "active" || ali.UsedTraffic != 47231285 || ali.DataLimit == nil || *ali.DataLimit != 4294967296 {
		t.Errorf("Ali.shvip = %+v", ali)
	}
	if ali.Note == nil || *ali.Note != "tab\there" {
		t.Errorf("note = %v, want the decoded tab", ali.Note)
	}
	if want := time.Date(2026, 10, 26, 13, 22, 52, 0, time.UTC).Unix(); ali.Expire == nil || int64(*ali.Expire) != want {
		t.Errorf("expire = %v, want unix %d", ali.Expire, want)
	}
	if ali.SubLastUserAgent == nil || *ali.SubLastUserAgent != "V2Box 10.1.8/iOS 17.5.1" ||
		ali.SubUpdatedAt == nil || !ali.SubUpdatedAt.Equal(time.Date(2026, 10, 1, 4, 12, 29, 568091000, time.UTC)) {
		t.Errorf("last subscription fetch = %v / %v, want the newest update row", ali.SubUpdatedAt, ali.SubLastUserAgent)
	}
	// Group "main" grants only the vless inbound: the vmess/trojan/
	// shadowsocks credentials PasarGuard generated anyway are not imported.
	if len(ali.Proxies) != 1 || ali.Proxies[0].Type != "vless" {
		t.Fatalf("Ali.shvip proxies = %+v, want vless only", ali.Proxies)
	}
	var vless map[string]string
	_ = json.Unmarshal(ali.Proxies[0].Settings, &vless)
	if vless["id"] != "688b1a71-fde2-4819-aa5d-b9d71dfa7780" || vless["flow"] != "" {
		t.Errorf("vless settings = %v, want the original UUID and no flow", vless)
	}

	pg := users["Parima.Gozarban"]
	if pg.SourceID != 1059 || pg.Status != "limited" || pg.DataLimitResetStrategy != "month" || pg.SubRevokedAt == nil || pg.Expire != nil {
		t.Errorf("Parima.Gozarban = %+v", pg)
	}
	_ = json.Unmarshal(pg.Proxies[0].Settings, &vless)
	if vless["flow"] != "xtls-rprx-vision" {
		t.Errorf("a stored Vision flow was not kept: %v", vless)
	}

	// Groups main + extra: vless and trojan (hysteria has no import, the
	// "Gone" inbound is defined in no core config).
	pz := users["Parima.gozarban"]
	if pz.SourceAdminID == nil || *pz.SourceAdminID != 2 || len(pz.Proxies) != 2 || pz.Proxies[0].Type != "vless" || pz.Proxies[1].Type != "trojan" {
		t.Errorf("Parima.gozarban = %+v, want admin 2 with vless+trojan", pz)
	}

	if len(users["nogroup"].Proxies) != 0 || len(users["frozen_only"].Proxies) != 0 {
		t.Errorf("users without an enabled group got proxies: %+v / %+v", users["nogroup"].Proxies, users["frozen_only"].Proxies)
	}
	fo := users["frozen_only"]
	if fo.Status != "on_hold" || fo.OnHoldExpireDuration == nil || *fo.OnHoldExpireDuration != 2592000 || fo.OnHoldTimeout == nil || fo.AutoDeleteInDays == nil || *fo.AutoDeleteInDays != 7 || fo.SourceAdminID != nil {
		t.Errorf("frozen_only = %+v", fo)
	}

	if len(data.NextPlans) != 1 || data.NextPlans[0].SourceUserID != 7 || data.NextPlans[0].DataLimit != 1073741824 ||
		data.NextPlans[0].Expire == nil || *data.NextPlans[0].Expire != 2592000 || !data.NextPlans[0].AddRemainingTraffic {
		t.Errorf("next plans = %+v", data.NextPlans)
	}

	all := strings.Join(data.Warnings, "\n")
	for _, want := range []string{
		`PasarGuard role "auditor"`,
		`admin "watcher" is disabled`,
		`"sub.watcher.example"`,
		"admin \"watcher\" had a traffic cap",
		`group "frozen" is disabled`,
		"2 user(s) had no enabled group",
		"1 user(s) had hysteria access",
		`inbound "Gone" is not defined in any core config`,
		`inbound "VLESS WS" (vless) served 3 user(s)`,
		`host "Live host" (cdn.example.com:443`,
		"1 user(s) had a device (HWID) limit",
		"1 registered device(s)",
		"https://sub.example.com:8443/<path>/<token>",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("warnings miss %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Old host") {
		t.Error("a disabled PasarGuard host was reported")
	}
}

func TestFromPasarGuardRejectsANonHexSecret(t *testing.T) {
	dump := strings.Replace(pasarGuardFixture(), pasarGuardTestSecret, "not-a-hex-secret", 1)
	d, err := ParsePgDump(strings.NewReader(dump), PasarGuardTables)
	if err != nil {
		t.Fatalf("ParsePgDump: %v", err)
	}
	data, err := FromPasarGuard(d)
	if err != nil {
		t.Fatalf("FromPasarGuard: %v", err)
	}
	if data.SubscriptionSecret != "" {
		t.Errorf("secret %q adopted, want none", data.SubscriptionSecret)
	}
}

// TestFromPasarGuardRealSample runs a real PasarGuard pg_dump (gzip-free
// .sql) through the parser and interpreter when PASARGUARD_SAMPLE_PG_DUMP
// points at one. Never commit such a file: it holds customer data and the
// panel's signing key.
func TestFromPasarGuardRealSample(t *testing.T) {
	path := os.Getenv("PASARGUARD_SAMPLE_PG_DUMP")
	if path == "" {
		t.Skip("PASARGUARD_SAMPLE_PG_DUMP not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	started := time.Now()
	d, err := ParsePgDump(f, PasarGuardTables)
	if err != nil {
		t.Fatalf("ParsePgDump: %v", err)
	}
	if !IsPasarGuardSchema(d.Created) {
		t.Fatal("real sample not recognized as PasarGuard")
	}
	data, err := FromPasarGuard(d)
	if err != nil {
		t.Fatalf("FromPasarGuard: %v", err)
	}
	proxies := map[string]int{}
	for _, u := range data.Users {
		for _, p := range u.Proxies {
			proxies[p.Type]++
		}
	}
	t.Logf("parsed in %v: %d admins, %d users, proxies %v, next plans %d, secret adopted=%v",
		time.Since(started), len(data.Admins), len(data.Users), proxies, len(data.NextPlans), data.SubscriptionSecret != "")
	for _, w := range data.Warnings {
		t.Logf("warning: %s", w)
	}
}
