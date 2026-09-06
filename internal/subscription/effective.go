package subscription

import (
	"crypto/ecdh"
	"encoding/base64"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// EffectiveInbound is the per-host, per-inbound merged view subscription
// generation actually consumes - the Go equivalent of the "host_inbound"
// dict app/subscription/share.py's process_inbounds_and_tags builds by
// layering a ProxyHost's overrides onto its ProxyInbound's own settings.
// Field-by-field precedence (host wins where it has an opinion, else the
// inbound's own value) matches that function exactly - see its comment
// block for which fields have no inbound-level fallback at all
// (MuxEnable, FragmentSetting, NoiseSetting, RandomUserAgent: host-only).
type EffectiveInbound struct {
	Tag        string
	Protocol   string
	Network    string
	HeaderType string
	Port       int
	Address    string
	SNI        string
	HostHeader string
	Path       string
	Security   string // none | tls | reality - resolved, never "inbound_default"

	ALPN          string
	Fingerprint   string
	AllowInsecure bool

	RealityPublicKey string
	RealityShortID   string

	MuxEnable       bool
	FragmentSetting string
	NoiseSetting    string
	RandomUserAgent bool
}

// BuildEffectiveInbound merges one Host row onto its parent Inbound row.
func BuildEffectiveInbound(inbound generated.Inbound, host generated.Host) EffectiveInbound {
	security := inbound.Security
	if host.Security != "inbound_default" {
		security = host.Security
	}

	// A host with no port set has nothing to fall back to - the inbounds
	// table doesn't carry a default listen port (see the migration notes
	// for why: this metadata is an interim sync stand-in, not a full
	// mirror of the live proxy config). In practice every real host has
	// its own port; this only matters for a misconfigured one.
	port := 0
	if host.Port.Valid {
		port = int(host.Port.Int32)
	}

	alpn := host.Alpn
	if alpn == "none" {
		alpn = ""
	}
	fingerprint := host.Fingerprint
	if fingerprint == "none" {
		fingerprint = ""
	}

	e := EffectiveInbound{
		Tag: inbound.Tag, Protocol: inbound.Protocol, Network: inbound.Network, HeaderType: inbound.HeaderType.String,
		Port: port, Address: host.Address, Security: security,
		SNI: valueOr(host.Sni, host.Address), HostHeader: valueOr(host.Host, ""),
		Path:          host.Path.String,
		ALPN:          alpn,
		Fingerprint:   fingerprint,
		AllowInsecure: host.Allowinsecure.Valid && host.Allowinsecure.Bool,
		MuxEnable:     host.MuxEnable, FragmentSetting: host.FragmentSetting.String, NoiseSetting: host.NoiseSetting.String,
		RandomUserAgent: host.RandomUserAgent,
	}
	if host.UseSniAsHost {
		e.HostHeader = e.SNI
	}

	if security == "reality" {
		e.RealityPublicKey = derivePublicKey(inbound.RealityPrivateKey.String)
		if len(inbound.RealityShortIds) > 0 {
			e.RealityShortID = inbound.RealityShortIds[0]
		}
	}
	return e
}

func valueOr(t pgtype.Text, fallback string) string {
	if t.Valid && t.String != "" {
		return t.String
	}
	return fallback
}

// derivePublicKey computes a REALITY public key from its base64url(no
// padding)-encoded X25519 private key - the same "pbk" value `xray x25519
// -i <private_key>` would print, needed since only the private key is
// stored (the public key is deterministic from it, no reason to store both).
func derivePublicKey(privateKeyB64 string) string {
	raw, err := base64.RawURLEncoding.DecodeString(privateKeyB64)
	if err != nil || len(raw) != 32 {
		return ""
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
}
