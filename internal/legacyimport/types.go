package legacyimport

import "time"

// ImportedData is the common intermediate shape every per-panel
// interpreter (marzban.go today, more later - see the Phase 8.2 plan)
// produces. internal/httpapi's loader writes exactly this shape into
// this panel's real Postgres schema, so adding a new source panel only
// ever means writing a new function that fills this struct correctly -
// the actual database-writing code is shared and never duplicated.
type ImportedData struct {
	Admins        []Admin
	Users         []User
	UserTemplates []UserTemplate
	Hosts         []Host
	Inbounds      []Inbound
	NextPlans     []NextPlan

	// SubscriptionSecret is the source panel's own signing key, empty when
	// the dump has none. Carrying it over is what keeps every subscriber's
	// EXISTING /sub/<token> link working after a migration: the token is
	// an HMAC over "<username>,<ts>" with this key, so a panel that
	// generates a fresh key instead silently invalidates every link already
	// installed in a customer's client app - they all 404 and every single
	// customer has to be handed a new URL.
	SubscriptionSecret string

	// Warnings surfaces anything the source panel had that couldn't be
	// carried over faithfully (an unrecognized proxy type, a malformed
	// JSON settings blob, a foreign key pointing at a row that turned out
	// not to exist) - each entry names the row so an admin can find and
	// fix it by hand afterward. An import with warnings still proceeds;
	// this is not a fatal-error list.
	Warnings []string
}

type Admin struct {
	// SourceID is the ID this admin had in the source panel - only used
	// in-memory to resolve User.SourceAdminID references during import;
	// never written to the new database (the new admins.id is a fresh
	// Postgres SERIAL value).
	SourceID        int64
	Username        string
	HashedPassword  string
	CreatedAt       time.Time
	IsSudo          bool
	PasswordResetAt *time.Time
	TelegramID      *int64
	DiscordWebhook  *string
	UsersUsage      int64
}

type Proxy struct {
	Type     string // already normalized to this codebase's lowercase enum: vmess/vless/trojan/shadowsocks
	Settings []byte // raw JSON, passed through proxysettings.FromStored for validation at load time

	// ExcludedInbounds lives per-proxy, not per-user, matching the source
	// schema exactly (exclude_inbounds_association.proxy_id references
	// proxies.id) - a user with more than one proxy can have different
	// exclusions on each.
	ExcludedInbounds []string
}

type User struct {
	// SourceID is this user's id in the source panel - used in-memory
	// during loading to resolve NextPlan.SourceUserID references; never
	// written to the new database.
	SourceID int64
	// SourceAdminID is the source panel's admin id this user belonged to,
	// resolved to the new admins.id via the Admin.SourceID map built
	// during loading. Nil means the user had no owning admin (matches the
	// source schema's own nullable admin_id).
	SourceAdminID *int64

	Username               string
	Status                 string
	UsedTraffic            int64
	DataLimit              *int64
	DataLimitResetStrategy string
	Expire                 *int32
	CreatedAt              time.Time
	Note                   *string
	SubRevokedAt           *time.Time
	SubUpdatedAt           *time.Time
	SubLastUserAgent       *string
	OnlineAt               *time.Time
	EditAt                 *time.Time
	OnHoldTimeout          *time.Time
	OnHoldExpireDuration   *int64
	AutoDeleteInDays       *int32
	LastStatusChange       *time.Time

	Proxies []Proxy
}

type UserTemplate struct {
	Name           string
	DataLimit      *int64
	ExpireDuration *int64
	UsernamePrefix *string
	UsernameSuffix *string
	InboundTags    []string
}

type Host struct {
	Remark          string
	Address         string
	Port            *int32
	InboundTag      string
	SNI             *string
	HostHeader      *string
	Security        string
	ALPN            string
	Fingerprint     string
	AllowInsecure   *bool
	IsDisabled      *bool
	Path            *string
	MuxEnable       bool
	FragmentSetting *string
	RandomUserAgent bool
	NoiseSetting    *string
	UseSNIAsHost    bool
}

// Inbound is deliberately minimal: the source schemas covered so far
// (Marzban/Rapido) never stored protocol/network/security/reality_* at
// the DB level at all (they lived only in the live Xray config file this
// import has no access to) - see the Phase 8.2 plan's explicit note on
// this gap. Every imported inbound needs its protocol configured by hand
// afterward through the Core Config / Inbounds UI before a node can
// actually use it; this is communicated via ImportedData.Warnings, not
// hidden.
type Inbound struct {
	Tag string
}

type NextPlan struct {
	// SourceUserID resolves to the new users.id the same way
	// User.SourceAdminID resolves admins - via the source panel's user id,
	// tracked in-memory during loading (see User, which doesn't carry its
	// own SourceID field since NextPlan is the only thing that needs to
	// look a user back up post-insert; the loader keeps its own
	// old-username-or-index -> new-id map instead of threading a field
	// through User for this one relationship).
	SourceUserID        int64
	DataLimit           int64
	Expire              *int32
	AddRemainingTraffic bool
	FireOnEither        bool
}
