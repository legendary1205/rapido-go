// Package xrayimport translates a real, raw Xray core-config JSON file
// (the exact shape the current/legacy Python panel writes to disk and an
// admin might have accumulated hand-edits in) into this codebase's own
// native inbound/host/core-config shapes - a pure, DB-free parser package,
// same architecture as internal/legacyimport (a parser + an intermediate
// result struct, with the actual database-writing code living in
// internal/httpapi and shared with the existing POST /api/inbounds/sync
// and PUT /api/settings/core-config write paths rather than duplicated
// here).
//
// Unlike internal/legacyimport (which truncates and fully replaces this
// panel's user data), an Xray-config import is purely additive: it only
// ever creates/upserts inbounds, their default hosts, and the fleet-wide
// Core Config - nothing here ever deletes a user, admin, or existing host
// customization.
package xrayimport

// Inbound mirrors internal/httpapi/inbounds.go's inboundSyncEntry field
// for field, plus HostALPN/HostFingerprint/Port - Xray's tlsSettings.alpn
// and .fingerprint live on the *inbound* in Xray's own shape, but on the
// *host* in this codebase's model (see internal/httpapi/hosts.go's
// hostDTO), so they travel here only to be handed to the synthesized
// default host, never written back onto the inbound row itself. Port is
// the single real port this entry represents - needed to give that
// synthesized host a real port (see ListAutoSyncInbounds's own doc
// comment on why a host with no port at all can never auto-sync).
type Inbound struct {
	Tag        string
	Protocol   string
	Network    string
	HeaderType string
	Security   string

	RealityPrivateKey string
	RealityShortIDs   []string
	RealityServerName string
	RealityServerPort int32

	TLSCertificate string
	TLSKey         string
	TLSServerName  string

	HostALPN        string
	HostFingerprint string
	Port            int32
}

// Outbound mirrors internal/httpapi/coreconfig.go's outboundDTO.
type Outbound struct {
	Tag        string
	Type       string
	Server     string
	ServerPort int
	Username   string
	Password   string
	Outbounds  []string

	UUID              string
	Flow              string
	Method            string
	Security          string
	CongestionControl string

	TLSEnabled    bool
	TLSServerName string
	TLSInsecure   bool

	BindInterface string
}

// RoutingRule mirrors internal/httpapi/coreconfig.go's routingRuleDTO.
type RoutingRule struct {
	Inbound       []string
	Domain        []string
	DomainSuffix  []string
	DomainKeyword []string
	IPCIDR        []string
	IPIsPrivate   bool
	Port          []int
	PortRange     []string
	Network       []string
	Protocol      []string
	OutboundTag   string
}

// DNSServer mirrors internal/httpapi/coreconfig.go's dnsServerDTO.
type DNSServer struct {
	Tag     string
	Type    string
	Address string
	Port    int
	Path    string
}

// Result is everything one Xray config file translates to. Warnings
// surfaces anything that couldn't be carried over faithfully (an
// unsupported field, a dropped fallback, more than one certificate on an
// inbound) - each entry names the specific inbound/outbound/rule so an
// admin can find and fix it by hand afterward. A parse with warnings still
// succeeds; this is not a fatal-error list, matching
// internal/legacyimport's ImportedData.Warnings convention exactly.
type Result struct {
	Inbounds     []Inbound
	Outbounds    []Outbound
	RoutingRules []RoutingRule
	DNSServers   []DNSServer
	LogLevel     string
	SniffEnabled bool
	Warnings     []string
}
