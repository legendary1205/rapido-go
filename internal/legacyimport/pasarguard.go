package legacyimport

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PasarGuard (github.com/PasarGuard/panel) is a from-scratch successor of
// Marzban, not a fork, and its schema shows it: a user's credentials for
// every protocol live in one users.proxy_settings JSON column (no proxies
// table), access is granted through groups (users_groups_association ->
// groups -> inbounds_groups_association) rather than Marzban's per-proxy
// exclusions, inbounds themselves are only defined inside Xray core configs
// (core_configs.config), and admins carry an RBAC role (admin_roles) instead
// of an is_sudo flag (older releases still had is_sudo; both are read).
//
// What this importer carries over, and why each part matters:
//   - every user with their ORIGINAL id (KeepUserIDs) - PasarGuard 5.x
//     subscription links are "v3,<user_id>,<ts>" HMACs, so a changed id is a
//     broken link (see subscription.ParseToken);
//   - the jwt.secret_key those links are signed with;
//   - each user's credentials (same UUIDs/passwords, so already-installed
//     client configs keep authenticating), but only for the protocols the
//     user could actually reach in PasarGuard - the protocols of the inbounds
//     in the user's enabled groups. PasarGuard fills proxy_settings with every
//     protocol for every user whether a group grants it or not;
//   - admins with their bcrypt hashes (same logins), mapped onto sudo/owner;
//   - last subscription fetch (user_subscription_updates) and next plans.
//
// What it deliberately does NOT carry over (UsersOnly): PasarGuard's
// inbounds and hosts. They are Xray inbound definitions, often on transports
// this panel's sing-box nodes don't serve (WebSocket, XHTTP...), so the
// operator's own inbounds on this panel stay in charge and every imported
// user gets them for the protocols they hold. The warnings list what
// PasarGuard had so it can be rebuilt by hand where wanted.

// pasarGuardSignature is the set of tables only a PasarGuard schema creates
// together - this panel's own dumps have core_config/jwt_secrets/
// goose_db_version instead, Marzban has no core_configs or groups.
var pasarGuardSignature = []string{"alembic_version", "admins", "users", "jwt", "core_configs", "users_groups_association"}

// IsPasarGuardSchema reports whether a dump's created tables are PasarGuard's.
func IsPasarGuardSchema(created map[string]bool) bool {
	for _, t := range pasarGuardSignature {
		if !created[t] {
			return false
		}
	}
	return true
}

var pasarGuardWanted = map[string]bool{
	"admins": true, "admin_roles": true, "users": true, "jwt": true, "groups": true,
	"users_groups_association": true, "inbounds": true, "inbounds_groups_association": true,
	"core_configs": true, "user_subscription_updates": true, "next_plans": true,
	"hosts": true, "user_hwids": true, "settings": true, "user_templates": true,
}

// PasarGuardTables is ParsePgDump's want filter for FromPasarGuard: only the
// tables it reads, so the bulky usage history (node_user_usages can run to
// millions of rows) is never held in memory.
func PasarGuardTables(table string) bool { return pasarGuardWanted[table] }

var lowerHexSecret = regexp.MustCompile(`^[0-9a-f]{64}$`)

// pasarGuardProxyOrder is the order proxies are created in - stable, so two
// imports of the same dump produce the same rows.
var pasarGuardProxyOrder = []string{"vless", "vmess", "trojan", "shadowsocks"}

// FromPasarGuard interprets a parsed PasarGuard dump. Like the other
// interpreters, one bad row becomes a warning, not an abort; it fails only
// when the dump has no users table at all.
func FromPasarGuard(d *PgDump) (ImportedData, error) {
	userRows, ok := d.Rows("users")
	if !ok {
		return ImportedData{}, fmt.Errorf("the PasarGuard dump has no users data")
	}
	data := ImportedData{KeepUserIDs: true, UsersOnly: true}
	warn := func(format string, a ...any) { data.Warnings = append(data.Warnings, fmt.Sprintf(format, a...)) }

	// --- signing key -------------------------------------------------
	if rows, _ := d.Rows("jwt"); len(rows) > 0 {
		secret := pgRow(rows[0]).str("secret_key")
		if lowerHexSecret.MatchString(secret) {
			data.SubscriptionSecret = secret
		} else {
			warn("PasarGuard's subscription signing key is not in the 64-character hex form this panel uses, so it was not adopted - every existing subscription link will stop working and customers need new ones")
		}
	} else {
		warn("the dump has no jwt row - every existing subscription link will stop working and customers need new ones")
	}

	// --- admins ------------------------------------------------------
	roles := map[int64]pgRow{}
	if rows, _ := d.Rows("admin_roles"); rows != nil {
		for _, r := range rows {
			if id, ok := pgRow(r).int64("id"); ok {
				roles[id] = r
			}
		}
	}
	adminRows, _ := d.Rows("admins")
	for _, raw := range adminRows {
		r := pgRow(raw)
		id, _ := r.int64("id")
		a := Admin{
			SourceID:        id,
			Username:        r.str("username"),
			HashedPassword:  r.str("hashed_password"),
			CreatedAt:       r.requiredTime("created_at", &data.Warnings, "admins", id),
			PasswordResetAt: r.time("password_reset_at"),
			TelegramID:      r.int64Ptr("telegram_id"),
			DiscordWebhook:  r.nonEmptyStrPtr("discord_webhook"),
		}
		if used, ok := r.int64("used_traffic"); ok {
			a.UsersUsage = used
		}
		if _, legacy := raw["is_sudo"]; legacy {
			a.IsSudo = r.bool("is_sudo")
		} else if roleID, ok := r.int64("role_id"); ok {
			role := roles[roleID]
			switch {
			case role != nil && role.bool("is_owner"):
				a.IsSudo, a.IsOwner = true, true
			case role != nil && role.str("name") == "administrator":
				a.IsSudo = true
			case role != nil && role.str("name") != "operator":
				warn("admin %q: PasarGuard role %q has no equivalent here - imported as a regular (non-sudo) admin who manages only their own users", a.Username, role.str("name"))
			}
		}
		if r.str("status") == "disabled" {
			warn("admin %q is disabled in PasarGuard; this panel has no disabled-admin state, so they can log in again here", a.Username)
		}
		if sd := r.str("sub_domain"); sd != "" {
			warn("admin %q had their own subscription domain %q in PasarGuard - their users' links point there, so that address must reach this panel too", a.Username, sd)
		}
		if r.has("data_limit") {
			warn("admin %q had a traffic cap in PasarGuard, which this panel has no equivalent for", a.Username)
		}
		data.Admins = append(data.Admins, a)
	}

	// --- what each group grants -------------------------------------
	protoByTag := pasarGuardInboundProtocols(d, warn)
	tagByInboundID := map[int64]string{}
	if rows, _ := d.Rows("inbounds"); rows != nil {
		for _, r := range rows {
			if id, ok := pgRow(r).int64("id"); ok {
				tagByInboundID[id] = pgRow(r).str("tag")
			}
		}
	}
	groupName := map[int64]string{}
	groupDisabled := map[int64]bool{}
	if rows, _ := d.Rows("groups"); rows != nil {
		for _, r := range rows {
			if id, ok := pgRow(r).int64("id"); ok {
				groupName[id] = pgRow(r).str("name")
				groupDisabled[id] = pgRow(r).bool("is_disabled")
			}
		}
	}
	groupTags := map[int64][]string{}
	if rows, _ := d.Rows("inbounds_groups_association"); rows != nil {
		for _, r := range rows {
			inboundID, ok1 := pgRow(r).int64("inbound_id")
			groupID, ok2 := pgRow(r).int64First("group_id", "groups_id")
			if ok1 && ok2 && tagByInboundID[inboundID] != "" {
				groupTags[groupID] = append(groupTags[groupID], tagByInboundID[inboundID])
			}
		}
	}
	userGroups := map[int64][]int64{}
	if rows, _ := d.Rows("users_groups_association"); rows != nil {
		for _, r := range rows {
			userID, ok1 := pgRow(r).int64("user_id")
			groupID, ok2 := pgRow(r).int64First("groups_id", "group_id")
			if ok1 && ok2 {
				userGroups[userID] = append(userGroups[userID], groupID)
			}
		}
	}

	// --- last subscription fetch per user ----------------------------
	type subFetch struct {
		at time.Time
		ua string
	}
	lastFetch := map[int64]subFetch{}
	if rows, _ := d.Rows("user_subscription_updates"); rows != nil {
		for _, r := range rows {
			userID, ok := pgRow(r).int64("user_id")
			at := pgRow(r).time("created_at")
			if !ok || at == nil {
				continue
			}
			if prev, seen := lastFetch[userID]; !seen || at.After(prev.at) {
				lastFetch[userID] = subFetch{at: *at, ua: pgRow(r).str("user_agent")}
			}
		}
	}

	defaultMethod := pasarGuardDefaultSSMethod(d)

	// --- users -------------------------------------------------------
	var noAccess, generatedSecrets, hwidLimited int
	unsupported := map[string]int{}
	unknownTags := map[string]bool{}
	tagUsers := map[string]int{}
	for _, raw := range userRows {
		r := pgRow(raw)
		id, ok := r.int64("id")
		if !ok || id < 1 || id > math.MaxInt32 {
			warn("user %q: id %s cannot be kept as this panel's user id, skipped", r.str("username"), r.str("id"))
			continue
		}
		u := User{
			SourceID:               id,
			SourceAdminID:          r.int64Ptr("admin_id"),
			Username:               r.str("username"),
			Status:                 r.str("status"),
			DataLimitResetStrategy: r.str("data_limit_reset_strategy"),
			CreatedAt:              r.requiredTime("created_at", &data.Warnings, "users", id),
			Note:                   r.strPtr("note"),
			SubRevokedAt:           r.time("sub_revoked_at"),
			OnlineAt:               r.time("online_at"),
			EditAt:                 r.time("edit_at"),
			OnHoldTimeout:          r.time("on_hold_timeout"),
			OnHoldExpireDuration:   r.int64Ptr("on_hold_expire_duration"),
			AutoDeleteInDays:       r.int32Ptr("auto_delete_in_days"),
			LastStatusChange:       r.time("last_status_change"),
			DataLimit:              r.int64Ptr("data_limit"),
		}
		u.UsedTraffic, _ = r.int64("used_traffic")
		switch u.Status {
		case "active", "disabled", "limited", "expired", "on_hold":
		default:
			warn("user %q: unknown status %q, imported as disabled", u.Username, u.Status)
			u.Status = "disabled"
		}
		switch u.DataLimitResetStrategy {
		case "no_reset", "day", "week", "month", "year":
		default:
			warn("user %q: unknown data limit reset strategy %q, imported as no_reset", u.Username, u.DataLimitResetStrategy)
			u.DataLimitResetStrategy = "no_reset"
		}
		if exp := r.time("expire"); exp != nil {
			if unix := exp.Unix(); unix > 0 && unix <= math.MaxInt32 {
				e := int32(unix)
				u.Expire = &e
			} else {
				warn("user %q: expiry %s is outside the range this panel stores, imported with no expiry", u.Username, exp.Format(time.RFC3339))
			}
		}
		if f, ok := lastFetch[id]; ok {
			at := f.at
			u.SubUpdatedAt = &at
			if f.ua != "" {
				ua := f.ua
				u.SubLastUserAgent = &ua
			}
		}
		if r.has("hwid_limit") {
			hwidLimited++
		}

		protos := map[string]bool{}
		reached := map[string]bool{}
		for _, g := range userGroups[id] {
			if groupDisabled[g] {
				continue
			}
			for _, tag := range groupTags[g] {
				if p, ok := protoByTag[tag]; ok {
					protos[p] = true
					reached[tag] = true
				} else {
					unknownTags[tag] = true
				}
			}
		}
		for tag := range reached {
			tagUsers[tag]++
		}
		var settings map[string]map[string]any
		if s := r.str("proxy_settings"); s != "" {
			if err := json.Unmarshal([]byte(s), &settings); err != nil {
				warn("user %q: unreadable proxy_settings, imported without credentials: %v", u.Username, err)
			}
		}
		for _, p := range pasarGuardProxyOrder {
			if !protos[p] {
				continue
			}
			proxy, generated := pasarGuardProxy(p, settings[p], defaultMethod)
			if generated {
				generatedSecrets++
			}
			u.Proxies = append(u.Proxies, proxy)
		}
		for p := range protos {
			if !containsString(pasarGuardProxyOrder, p) {
				unsupported[p]++
			}
		}
		if len(u.Proxies) == 0 {
			noAccess++
		}
		data.Users = append(data.Users, u)
	}

	// --- next plans --------------------------------------------------
	if rows, _ := d.Rows("next_plans"); rows != nil {
		for _, raw := range rows {
			r := pgRow(raw)
			userID, ok := r.int64("user_id")
			if !ok {
				continue
			}
			np := NextPlan{SourceUserID: userID, Expire: r.int32Ptr("expire"), AddRemainingTraffic: r.bool("add_remaining_traffic"), FireOnEither: true}
			np.DataLimit, _ = r.int64("data_limit")
			if r.has("user_template_id") {
				warn("user id %d: next plan came from a PasarGuard user template, which is not carried over - only its data limit and duration are", userID)
			}
			data.NextPlans = append(data.NextPlans, np)
		}
	}

	// --- what was left behind ---------------------------------------
	disabledMembers := map[string]int{}
	for _, groups := range userGroups {
		for _, g := range groups {
			if groupDisabled[g] {
				disabledMembers[groupName[g]]++
			}
		}
	}
	for _, name := range sortedKeys(disabledMembers) {
		warn("PasarGuard group %q is disabled, so it granted its %d member(s) nothing - the same holds here", name, disabledMembers[name])
	}
	if noAccess > 0 {
		warn("%d user(s) had no enabled group granting an inbound in PasarGuard; they were imported without any proxy, so their subscription stays empty until one is added", noAccess)
	}
	if generatedSecrets > 0 {
		warn("%d credential(s) were missing from PasarGuard's proxy_settings and were generated fresh - those users need to refresh their subscription", generatedSecrets)
	}
	for _, p := range sortedKeys(unsupported) {
		warn("%d user(s) had %s access in PasarGuard; this panel has no %s import, so it was not carried over", unsupported[p], p, p)
	}
	for _, tag := range sortedKeys(unknownTags) {
		warn("PasarGuard group inbound %q is not defined in any core config, so it granted nothing and was ignored", tag)
	}
	for _, tag := range sortedKeys(tagUsers) {
		warn("PasarGuard inbound %q (%s) served %d user(s); PasarGuard inbounds are not recreated here - those users get this panel's own %s inbound(s)", tag, protoByTag[tag], tagUsers[tag], protoByTag[tag])
	}
	if hostRows, _ := d.Rows("hosts"); hostRows != nil {
		for _, raw := range hostRows {
			r := pgRow(raw)
			if r.bool("is_disabled") {
				continue
			}
			warn("PasarGuard host %q (%s:%s, inbound %q) was not recreated - this panel's own hosts are kept", r.str("remark"), r.str("address"), r.str("port"), r.str("inbound_tag"))
		}
	}
	if rows, _ := d.Rows("user_templates"); len(rows) > 0 {
		warn("%d PasarGuard user template(s) were not carried over - this panel's own templates are kept", len(rows))
	}
	if hwidLimited > 0 {
		warn("%d user(s) had a device (HWID) limit in PasarGuard; this panel has no device limits, so they are unlimited here", hwidLimited)
	}
	if rows, _ := d.Rows("user_hwids"); len(rows) > 0 {
		warn("%d registered device(s) (user_hwids) were not carried over", len(rows))
	}
	if prefix := pasarGuardSubURLPrefix(d); prefix != "" {
		warn("PasarGuard handed out subscription links under %s/<path>/<token> (path from its .env, \"sub\" by default) - point that address at this panel and set XRAY_SUBSCRIPTION_URL_PREFIX to it, so existing links reach this panel and new ones look the same", prefix)
	}
	return data, nil
}

// pasarGuardInboundProtocols maps every inbound tag defined in any Xray core
// config to its protocol. A tag defined twice with different protocols keeps
// the first and says so.
func pasarGuardInboundProtocols(d *PgDump, warn func(string, ...any)) map[string]string {
	out := map[string]string{}
	rows, _ := d.Rows("core_configs")
	for _, raw := range rows {
		r := pgRow(raw)
		if t := r.str("type"); t != "" && t != "xray" {
			continue
		}
		var cfg struct {
			Inbounds []struct {
				Tag      string `json:"tag"`
				Protocol string `json:"protocol"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal([]byte(r.str("config")), &cfg); err != nil {
			warn("PasarGuard core config %q is not valid JSON, its inbounds were ignored: %v", r.str("name"), err)
			continue
		}
		for _, in := range cfg.Inbounds {
			if in.Tag == "" {
				continue
			}
			p := strings.ToLower(in.Protocol)
			if prev, ok := out[in.Tag]; ok && prev != p {
				warn("PasarGuard inbound %q is %s in one core config and %s in another; treated as %s", in.Tag, prev, p, prev)
				continue
			}
			out[in.Tag] = p
		}
	}
	return out
}

// pasarGuardProxy builds one proxies.settings value from a user's
// proxy_settings entry, keeping the exact secret so installed configs keep
// working. A missing secret is generated (reported by the second result).
func pasarGuardProxy(protocol string, src map[string]any, defaultMethod string) (Proxy, bool) {
	str := func(k string) string { s, _ := src[k].(string); return s }
	generated := false
	var settings map[string]any
	switch protocol {
	case "vless":
		id := str("id")
		if id == "" {
			id, generated = uuid.NewString(), true
		}
		settings = map[string]any{"id": id, "flow": str("flow")}
	case "vmess":
		id := str("id")
		if id == "" {
			id, generated = uuid.NewString(), true
		}
		settings = map[string]any{"id": id}
	case "trojan":
		pw := str("password")
		if pw == "" {
			pw, generated = uuid.NewString(), true
		}
		settings = map[string]any{"password": pw, "flow": ""}
	case "shadowsocks":
		pw := str("password")
		if pw == "" {
			pw, generated = uuid.NewString(), true
		}
		method := str("method")
		if method == "" {
			method = defaultMethod
		}
		settings = map[string]any{"password": pw, "method": method}
	}
	raw, _ := json.Marshal(settings)
	return Proxy{Type: protocol, Settings: raw}, generated
}

// pasarGuardDefaultSSMethod is settings.general.default_method - what
// PasarGuard falls back to for a shadowsocks user stored without a method.
func pasarGuardDefaultSSMethod(d *PgDump) string {
	if rows, _ := d.Rows("settings"); len(rows) > 0 {
		var general struct {
			DefaultMethod string `json:"default_method"`
		}
		if json.Unmarshal([]byte(pgRow(rows[0]).str("general")), &general) == nil && general.DefaultMethod != "" {
			return general.DefaultMethod
		}
	}
	return "chacha20-ietf-poly1305"
}

// pasarGuardSubURLPrefix is settings.subscription.url_prefix, the base of
// every subscription link PasarGuard handed out.
func pasarGuardSubURLPrefix(d *PgDump) string {
	if rows, _ := d.Rows("settings"); len(rows) > 0 {
		var sub struct {
			URLPrefix string `json:"url_prefix"`
		}
		if json.Unmarshal([]byte(pgRow(rows[0]).str("subscription")), &sub) == nil {
			return strings.TrimRight(sub.URLPrefix, "/")
		}
	}
	return ""
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- row value helpers ------------------------------------------------
//
// pgRow wraps one ParsePgDump row: every value is a column's text form, nil
// for SQL NULL.

type pgRow map[string]*string

func (r pgRow) has(col string) bool { return r[col] != nil }

func (r pgRow) str(col string) string {
	if v := r[col]; v != nil {
		return *v
	}
	return ""
}

func (r pgRow) strPtr(col string) *string {
	if v := r[col]; v != nil {
		s := *v
		return &s
	}
	return nil
}

func (r pgRow) nonEmptyStrPtr(col string) *string {
	if v := r[col]; v != nil && *v != "" {
		s := *v
		return &s
	}
	return nil
}

func (r pgRow) int64(col string) (int64, bool) {
	v := r[col]
	if v == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(*v, 10, 64)
	return n, err == nil
}

// int64First reads the first of cols present - for a column PasarGuard has
// spelled differently across releases.
func (r pgRow) int64First(cols ...string) (int64, bool) {
	for _, c := range cols {
		if n, ok := r.int64(c); ok {
			return n, true
		}
	}
	return 0, false
}

func (r pgRow) int64Ptr(col string) *int64 {
	if n, ok := r.int64(col); ok {
		return &n
	}
	return nil
}

func (r pgRow) int32Ptr(col string) *int32 {
	if n, ok := r.int64(col); ok && n >= math.MinInt32 && n <= math.MaxInt32 {
		v := int32(n)
		return &v
	}
	return nil
}

func (r pgRow) bool(col string) bool {
	switch r.str(col) {
	case "t", "true", "1":
		return true
	}
	return false
}

// pgTimeLayouts covers how Postgres prints timestamptz under the default
// ISO DateStyle (offset as +00, +03:30 or +05:30:15) and plain timestamp
// (read as UTC). Go accepts the optional fractional seconds on its own.
var pgTimeLayouts = []string{
	"2006-01-02 15:04:05-07",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05-07:00:00",
	"2006-01-02 15:04:05",
}

func (r pgRow) time(col string) *time.Time {
	s := r.str(col)
	if s == "" {
		return nil
	}
	for _, layout := range pgTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

func (r pgRow) requiredTime(col string, warnings *[]string, table string, id int64) time.Time {
	if t := r.time(col); t != nil {
		return *t
	}
	*warnings = append(*warnings, fmt.Sprintf("%s: row %d missing %s, defaulted to the import time", table, id, col))
	return time.Now().UTC()
}
