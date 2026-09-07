package legacyimport

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/legendary1205/rapido-go/internal/auth"
)

// hiddifyProtoMap normalizes Hiddify's proto enum to this codebase's
// lowercase one. "ss" is Hiddify's real value for Shadowsocks (confirmed
// against a live v12.3.3 export, not guessed) - every other proto Hiddify
// supports (v2ray, ssh, wireguard, tuic, hysteria2, naive, mieru, dnstt)
// has no equivalent in proxysettings.ProxyType and is dropped with a
// warning.
var hiddifyProtoMap = map[string]string{
	"vless":  "vless",
	"vmess":  "vmess",
	"trojan": "trojan",
	"ss":     "shadowsocks",
}

// oneGiB matches Hiddify's own ONE_GIG constant (binary gigabyte) - its
// export reports usage/limits in GB (a float), not raw bytes like this
// codebase's own schema.
const oneGiB = 1024 * 1024 * 1024

// hiddifyDataLimitResetMap mirrors data_limit_reset_strategy's CHECK
// constraint (no_reset/day/week/month/year) - Hiddify's own UserMode enum
// covers everything except "year", which it has no concept of.
var hiddifyDataLimitResetMap = map[string]string{
	"no_reset": "no_reset",
	"daily":    "day",
	"weekly":   "week",
	"monthly":  "month",
}

// hiddifyExport mirrors hiddifypanel.panel.hiddify.dump_db_to_dict()'s real
// output shape - field names confirmed against a live v12.3.3 instance's
// actual `hiddifypanel backup` output (deployed and seeded specifically for
// this), not guessed from the model source alone. That mattered here more
// than for Marzban: Hiddify's proxy model is a set of GLOBAL proto/
// transport/l3 combinations shared by every user (not per-user rows like
// Marzban's proxies table), each user carries a single `uuid` used across
// every enabled combination, and usage/limits are serialized in GB
// (float), not bytes - all three would have been easy to get wrong without
// a real sample to check against.
type hiddifyExport struct {
	AdminUsers []hiddifyAdminUser `json:"admin_users"`
	Users      []hiddifyUser      `json:"users"`
	Domains    []hiddifyDomain    `json:"domains"`
	Proxies    []hiddifyProxy     `json:"proxies"`
}

type hiddifyAdminUser struct {
	UUID            string `json:"uuid"`
	Name            string `json:"name"`
	Mode            string `json:"mode"` // super_admin / admin / agent
	ParentAdminUUID string `json:"parent_admin_uuid"`
}

type hiddifyUser struct {
	UUID         string  `json:"uuid"`
	Name         string  `json:"name"`
	AddedByUUID  string  `json:"added_by_uuid"`
	Enable       bool    `json:"enable"`
	CurrentUsage float64 `json:"current_usage_GB"`
	UsageLimit   float64 `json:"usage_limit_GB"`
	PackageDays  int64   `json:"package_days"`
	StartDate    *string `json:"start_date"` // "YYYY-MM-DD", null if the package hasn't started yet
	Mode         string  `json:"mode"`       // no_reset / monthly / weekly / daily
}

type hiddifyDomain struct {
	Domain string `json:"domain"`
}

// hiddifyProxy is one row of Hiddify's global proto/transport/l3 catalog -
// not a per-user credential (see hiddifyExport's own doc comment).
type hiddifyProxy struct {
	Enable    bool   `json:"enable"`
	Proto     string `json:"proto"`
	Transport string `json:"transport"`
	L3        string `json:"l3"`
}

// FromHiddifyJSON interprets a real `hiddifypanel backup` JSON export into
// the common ImportedData shape. Unlike FromMarzbanMySQLDump, this source
// schema DOES track protocol/network/security explicitly (Marzban's never
// stored that at the DB level at all) - every synthesized Inbound here
// gets a real, usable protocol/network/security rather than a "vless"
// placeholder, a genuine improvement over the Marzban baseline rather than
// a gap.
func FromHiddifyJSON(raw []byte) (ImportedData, error) {
	var src hiddifyExport
	if err := json.Unmarshal(raw, &src); err != nil {
		return ImportedData{}, fmt.Errorf("invalid Hiddify export JSON: %w", err)
	}

	var out ImportedData

	// Hiddify cross-references admins/users by uuid, not by a small
	// integer id (see the doc comment above) - ImportedData.Admin/User
	// still key off an int64 SourceID/SourceAdminID (the shape every
	// interpreter shares), so a synthetic sequential id is assigned here,
	// purely in-memory, and never written anywhere.
	adminIDByUUID := make(map[string]int64, len(src.AdminUsers))
	var nextAdminID int64 = 1
	for _, a := range src.AdminUsers {
		id := nextAdminID
		nextAdminID++
		adminIDByUUID[a.UUID] = id

		if a.ParentAdminUUID != "" && a.ParentAdminUUID != a.UUID {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"admin %q: Hiddify's reseller hierarchy (this admin's parent) has no equivalent here and was dropped - imported as a flat, independent admin",
				a.Name))
		}

		plainPassword, err := randomPassword()
		if err != nil {
			return ImportedData{}, fmt.Errorf("could not generate a password for admin %q: %w", a.Name, err)
		}
		hashed, err := auth.HashPassword(plainPassword)
		if err != nil {
			return ImportedData{}, fmt.Errorf("could not hash a password for admin %q: %w", a.Name, err)
		}
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"admin %q: Hiddify has no username/password login (its export carries no password at all) - a temporary password was generated: %s - sign in and change it immediately",
			a.Name, plainPassword))

		out.Admins = append(out.Admins, Admin{
			SourceID:       id,
			Username:       a.Name,
			HashedPassword: hashed,
			CreatedAt:      time.Now().UTC(),
			IsSudo:         a.Mode == "super_admin",
		})
	}

	// Distinct (proto, transport, l3) combinations, restricted to enabled
	// rows and protocols this codebase actually supports - each becomes
	// one Inbound tag. Also tracks which supported protocols exist at all,
	// since every user gets one Proxy per protocol found here (see below).
	type inboundKey struct{ proto, transport, l3 string }
	seenInbounds := map[inboundKey]bool{}
	supportedProtos := map[string]bool{}
	for _, p := range src.Proxies {
		if !p.Enable {
			continue
		}
		newType, ok := hiddifyProtoMap[p.Proto]
		if !ok {
			continue
		}
		supportedProtos[newType] = true
		key := inboundKey{p.Proto, strings.ToLower(p.Transport), p.L3}
		if seenInbounds[key] {
			continue
		}
		seenInbounds[key] = true
		out.Inbounds = append(out.Inbounds, Inbound{
			Tag: fmt.Sprintf("%s-%s-%s", p.Proto, strings.ToLower(p.Transport), p.L3),
		})
	}
	if len(supportedProtos) == 0 && len(src.Proxies) > 0 {
		out.Warnings = append(out.Warnings, "proxies: none of Hiddify's enabled protocols (vless/vmess/trojan/ss) were found - every proxy in this export uses a protocol this panel doesn't support (v2ray/ssh/wireguard/tuic/hysteria2/naive/mieru/dnstt)")
	}

	if len(src.Domains) > 0 {
		names := make([]string, 0, len(src.Domains))
		for _, d := range src.Domains {
			if d.Domain != "" {
				names = append(names, d.Domain)
			}
		}
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"domains: Hiddify's domains (%s) aren't linked to a specific protocol in its own schema, so they were not turned into hosts automatically - add them by hand on the Hosts page for whichever inbound(s) they actually serve",
			strings.Join(names, ", ")))
	}

	out.Warnings = append(out.Warnings,
		"hconfigs: Hiddify's global settings (Reality keys, TLS/mux/wireguard options) were not imported - configure TLS/Reality for the new inbounds from the Inbounds page, same as every other imported source")

	now := time.Now()
	var nextUserID int64 = 1
	for _, u := range src.Users {
		userSourceID := nextUserID
		nextUserID++

		var adminID *int64
		if u.AddedByUUID != "" {
			if aid, ok := adminIDByUUID[u.AddedByUUID]; ok {
				adminID = &aid
			}
		}

		var expire *int32
		if u.StartDate != nil && *u.StartDate != "" {
			if start, err := time.Parse("2006-01-02", *u.StartDate); err == nil {
				expUnix := start.AddDate(0, 0, int(u.PackageDays)).Unix()
				// expire is int32 (matches this codebase's own users.expire
				// column, inherited from Marzban's own schema) - a very
				// long package_days (some panels use e.g. 36500 for a
				// "lifetime" plan) can push the resulting date's Unix
				// timestamp past int32's range. Truncating it silently
				// would wrap into an arbitrary, possibly-past value and
				// mark a real, paid-up user as expired - drop the expiry
				// instead and say so, rather than risk that.
				if expUnix > math.MaxInt32 {
					out.Warnings = append(out.Warnings, fmt.Sprintf(
						"user %q: start_date+package_days lands too far in the future to store, imported with no expiry", u.Name))
				} else {
					exp := int32(expUnix)
					expire = &exp
				}
			} else {
				out.Warnings = append(out.Warnings, fmt.Sprintf(
					"user %q: start_date %q could not be parsed, imported with no expiry", u.Name, *u.StartDate))
			}
		}

		status := "active"
		switch {
		case !u.Enable:
			status = "disabled"
		case u.UsageLimit > 0 && u.CurrentUsage*oneGiB >= u.UsageLimit*oneGiB:
			status = "limited"
		case expire != nil && int64(*expire) < now.Unix():
			status = "expired"
		}

		var dataLimit *int64
		if u.UsageLimit > 0 {
			dl := int64(u.UsageLimit * oneGiB)
			dataLimit = &dl
		}

		resetStrategy, ok := hiddifyDataLimitResetMap[u.Mode]
		if !ok {
			resetStrategy = "no_reset"
		}

		var proxies []Proxy
		for proto := range supportedProtos {
			settings, err := hiddifyProxySettingsJSON(proto, u.UUID)
			if err != nil {
				out.Warnings = append(out.Warnings, fmt.Sprintf(
					"user %q: could not build %s settings: %v", u.Name, proto, err))
				continue
			}
			proxies = append(proxies, Proxy{Type: proto, Settings: settings})
		}

		out.Users = append(out.Users, User{
			SourceID:               userSourceID,
			SourceAdminID:          adminID,
			Username:               u.Name,
			Status:                 status,
			UsedTraffic:            int64(u.CurrentUsage * oneGiB),
			DataLimit:              dataLimit,
			DataLimitResetStrategy: resetStrategy,
			Expire:                 expire,
			CreatedAt:              now,
			Proxies:                proxies,
		})
	}
	if len(src.Users) > 0 {
		out.Warnings = append(out.Warnings,
			"users/admins: Hiddify's export doesn't include creation timestamps - every imported account shows the import time as its created date")
	}

	return out, nil
}

// hiddifyProxySettingsJSON builds the exact JSON shape
// internal/proxysettings expects, keyed off the single uuid Hiddify gives
// each user - vmess/vless use it as the client id, trojan/shadowsocks as
// the password, matching how Hiddify itself plugs that same uuid into
// every enabled protocol's share link.
func hiddifyProxySettingsJSON(proto, uuid string) ([]byte, error) {
	switch proto {
	case "vmess", "vless":
		return json.Marshal(map[string]string{"id": uuid})
	case "trojan", "shadowsocks":
		return json.Marshal(map[string]string{"password": uuid})
	default:
		return nil, fmt.Errorf("unhandled protocol %q", proto)
	}
}

// randomPassword mirrors proxysettings.randomPassword's own convention
// (16 random bytes, unpadded base64url) - used here for the admin
// passwords Hiddify's export has no equivalent of at all.
func randomPassword() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
