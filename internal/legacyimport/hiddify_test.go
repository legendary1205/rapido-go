package legacyimport

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// hiddifyFixture is a small synthetic export shaped like a real
// `hiddifypanel backup` output (see hiddify.go's own doc comment on
// FromHiddifyJSON for how the real field names were confirmed) - fictional
// values throughout, no real customer data or keys.
const hiddifyFixture = `{
  "admin_users": [
    {"uuid": "owner-uuid", "name": "Owner", "mode": "super_admin", "parent_admin_uuid": "owner-uuid"},
    {"uuid": "reseller-uuid", "name": "Reseller One", "mode": "admin", "parent_admin_uuid": "owner-uuid"}
  ],
  "users": [
    {
      "uuid": "user-active-uuid", "name": "active_user", "added_by_uuid": "reseller-uuid",
      "enable": true, "current_usage_GB": 1.5, "usage_limit_GB": 50,
      "package_days": 30, "start_date": "2030-01-01", "mode": "monthly"
    },
    {
      "uuid": "user-disabled-uuid", "name": "disabled_user", "added_by_uuid": "owner-uuid",
      "enable": false, "current_usage_GB": 0.1, "usage_limit_GB": 10,
      "package_days": 30, "start_date": "2020-01-01", "mode": "no_reset"
    },
    {
      "uuid": "user-notstarted-uuid", "name": "not_started_user", "added_by_uuid": "unknown-uuid",
      "enable": true, "current_usage_GB": 0, "usage_limit_GB": 0,
      "package_days": 30, "start_date": null, "mode": "no_reset"
    }
  ],
  "domains": [
    {"domain": "vpn.example.test"}
  ],
  "proxies": [
    {"enable": true, "proto": "vless", "transport": "tcp", "l3": "tls"},
    {"enable": true, "proto": "vmess", "transport": "ws", "l3": "tls"},
    {"enable": false, "proto": "trojan", "transport": "grpc", "l3": "tls"},
    {"enable": true, "proto": "wireguard", "transport": "udp", "l3": "udp"}
  ]
}`

func TestFromHiddifyJSON(t *testing.T) {
	data, err := FromHiddifyJSON([]byte(hiddifyFixture))
	if err != nil {
		t.Fatalf("FromHiddifyJSON: %v", err)
	}

	// Two admins: the root (self-referential parent, no warning) and the
	// reseller (real parent -> warning). Both get a freshly generated,
	// bcrypt-hashed password since Hiddify's export has no password field.
	if len(data.Admins) != 2 {
		t.Fatalf("Admins = %+v", data.Admins)
	}
	for _, a := range data.Admins {
		if a.HashedPassword == "" {
			t.Errorf("admin %q has no generated password", a.Username)
		}
	}
	owner, reseller := data.Admins[0], data.Admins[1]
	if owner.Username != "Owner" || !owner.IsSudo {
		t.Errorf("owner = %+v, want sudo admin named Owner", owner)
	}
	if reseller.Username != "Reseller One" || reseller.IsSudo {
		t.Errorf("reseller = %+v, want non-sudo admin named Reseller One", reseller)
	}

	if len(data.Users) != 3 {
		t.Fatalf("Users = %+v", data.Users)
	}
	active, disabled, notStarted := data.Users[0], data.Users[1], data.Users[2]

	if active.Status != "active" {
		t.Errorf("active_user status = %q, want active", active.Status)
	}
	if active.SourceAdminID == nil || *active.SourceAdminID != reseller.SourceID {
		t.Errorf("active_user.SourceAdminID = %v, want resolved to reseller (%d)", active.SourceAdminID, reseller.SourceID)
	}
	if active.DataLimitResetStrategy != "month" {
		t.Errorf("active_user reset strategy = %q, want month (from Hiddify's monthly)", active.DataLimitResetStrategy)
	}
	if active.Expire == nil {
		t.Fatal("active_user has no expiry, want start_date+package_days computed")
	}
	if active.DataLimit == nil || *active.DataLimit != 50*oneGiB {
		t.Errorf("active_user.DataLimit = %v, want 50 GiB in bytes", active.DataLimit)
	}
	if active.UsedTraffic != int64(1.5*oneGiB) {
		t.Errorf("active_user.UsedTraffic = %d, want 1.5 GiB in bytes", active.UsedTraffic)
	}

	if disabled.Status != "disabled" {
		t.Errorf("disabled_user status = %q, want disabled (enable=false wins over everything else)", disabled.Status)
	}

	if notStarted.Expire != nil {
		t.Errorf("not_started_user.Expire = %v, want nil (start_date is null)", notStarted.Expire)
	}
	if notStarted.DataLimit != nil {
		t.Errorf("not_started_user.DataLimit = %v, want nil (usage_limit_GB is 0, meaning unlimited)", notStarted.DataLimit)
	}
	if notStarted.SourceAdminID != nil {
		t.Errorf("not_started_user.SourceAdminID = %v, want nil (added_by_uuid references an admin not in this export)", notStarted.SourceAdminID)
	}

	// Proxies: vless+vmess are enabled and supported -> every user gets one
	// of each, keyed by their own uuid. trojan is disabled (skipped
	// entirely) and wireguard has no rapido-go equivalent, so neither
	// should produce a Proxy for anyone.
	if len(active.Proxies) != 2 {
		t.Fatalf("active_user.Proxies = %+v, want exactly vless+vmess", active.Proxies)
	}
	seenTypes := map[string]string{}
	for _, p := range active.Proxies {
		seenTypes[p.Type] = string(p.Settings)
	}
	var vless struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(seenTypes["vless"]), &vless); err != nil || vless.ID != "user-active-uuid" {
		t.Errorf("active_user vless settings = %q, want id=user-active-uuid", seenTypes["vless"])
	}
	if _, ok := seenTypes["vmess"]; !ok {
		t.Errorf("active_user.Proxies = %+v, want a vmess entry too", active.Proxies)
	}

	// Inbound tags: one per distinct enabled+supported (proto,transport,l3)
	// - trojan (disabled) and wireguard (unsupported) must not appear.
	if len(data.Inbounds) != 2 {
		t.Fatalf("Inbounds = %+v", data.Inbounds)
	}
	tags := map[string]bool{}
	for _, ib := range data.Inbounds {
		tags[ib.Tag] = true
	}
	if !tags["vless-tcp-tls"] || !tags["vmess-ws-tls"] {
		t.Errorf("Inbounds = %+v, want vless-tcp-tls and vmess-ws-tls", data.Inbounds)
	}

	foundHierarchyWarning := false
	foundDomainsWarning := false
	foundPasswordWarning := false
	for _, w := range data.Warnings {
		switch {
		case strings.Contains(w, "Reseller One") && strings.Contains(w, "hierarchy"):
			foundHierarchyWarning = true
		case strings.Contains(w, "vpn.example.test"):
			foundDomainsWarning = true
		case strings.Contains(w, "temporary password"):
			foundPasswordWarning = true
		}
	}
	if !foundHierarchyWarning {
		t.Error("expected a warning about the reseller's dropped parent-admin hierarchy")
	}
	if !foundDomainsWarning {
		t.Error("expected a warning naming the domain that wasn't auto-attached to a host")
	}
	if !foundPasswordWarning {
		t.Error("expected a warning surfacing each admin's generated temporary password")
	}
}

// TestFromHiddifyJSONClampsAnExpiryTooFarInTheFuture covers a real Hiddify
// pattern (a "lifetime" plan set via a very large package_days, e.g.
// 36500) that would otherwise overflow this codebase's int32 expire
// column into an arbitrary, possibly-past value.
func TestFromHiddifyJSONClampsAnExpiryTooFarInTheFuture(t *testing.T) {
	const fixture = `{
		"admin_users": [{"uuid": "a", "name": "Owner", "mode": "super_admin", "parent_admin_uuid": "a"}],
		"users": [{
			"uuid": "u1", "name": "lifetime_user", "added_by_uuid": "a",
			"enable": true, "current_usage_GB": 0, "usage_limit_GB": 0,
			"package_days": 36500, "start_date": "2026-01-01", "mode": "no_reset"
		}],
		"domains": [], "proxies": []
	}`
	data, err := FromHiddifyJSON([]byte(fixture))
	if err != nil {
		t.Fatalf("FromHiddifyJSON: %v", err)
	}
	if len(data.Users) != 1 {
		t.Fatalf("Users = %+v", data.Users)
	}
	if data.Users[0].Expire != nil {
		t.Errorf("lifetime_user.Expire = %v, want nil (too far in the future for int32)", *data.Users[0].Expire)
	}
	found := false
	for _, w := range data.Warnings {
		if strings.Contains(w, "lifetime_user") && strings.Contains(w, "too far in the future") {
			found = true
		}
	}
	if !found {
		t.Error("expected a warning explaining why lifetime_user has no expiry")
	}
}

// TestFromHiddifyJSONRealSample runs the full parser against a real
// `hiddifypanel backup` export if LEGACY_SAMPLE_HIDDIFY_JSON is set (same
// pattern as TestFromMarzbanMySQLDumpRealSample) - proves the interpreter
// handles a real install's actual data shape, without committing it or
// asserting on any specific real value.
func TestFromHiddifyJSONRealSample(t *testing.T) {
	path := os.Getenv("LEGACY_SAMPLE_HIDDIFY_JSON")
	if path == "" {
		t.Skip("LEGACY_SAMPLE_HIDDIFY_JSON not set, skipping real-sample interpret test")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	data, err := FromHiddifyJSON(raw)
	if err != nil {
		t.Fatalf("FromHiddifyJSON: %v", err)
	}

	t.Logf("admins=%d users=%d inbounds=%d warnings=%d",
		len(data.Admins), len(data.Users), len(data.Inbounds), len(data.Warnings))

	if len(data.Admins) == 0 || len(data.Users) == 0 {
		t.Fatal("expected a real sample to produce at least one admin and one user")
	}
	for _, u := range data.Users {
		if u.Username == "" {
			t.Error("a user was imported with an empty username")
		}
		switch u.Status {
		case "active", "limited", "expired", "disabled":
		default:
			t.Errorf("user %q has unexpected status %q", u.Username, u.Status)
		}
		for _, p := range u.Proxies {
			switch p.Type {
			case "vmess", "vless", "trojan", "shadowsocks":
			default:
				t.Errorf("user %q has proxy with unexpected type %q", u.Username, p.Type)
			}
		}
	}
	for _, w := range data.Warnings {
		t.Logf("warning: %s", w)
	}
}
