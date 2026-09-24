// Package proxysettings mirrors app/models/proxy.py's per-protocol
// ProxySettings classes: the exact JSON shape stored in proxies.settings,
// with the same auto-generation and "revoke" (rotate secret) behavior.
package proxysettings

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

type ProxyType string

const (
	VMess       ProxyType = "vmess"
	VLESS       ProxyType = "vless"
	Trojan      ProxyType = "trojan"
	Shadowsocks ProxyType = "shadowsocks"
)

func (t ProxyType) Valid() bool {
	switch t {
	case VMess, VLESS, Trojan, Shadowsocks:
		return true
	}
	return false
}

// XTLSFlow mirrors xray_api.types.account.XTLSFlows.
type XTLSFlow string

const (
	FlowNone   XTLSFlow = ""
	FlowVision XTLSFlow = "xtls-rprx-vision"
)

// ShadowsocksMethod mirrors xray_api.types.account.ShadowsocksMethods.
type ShadowsocksMethod string

const (
	AES128GCM        ShadowsocksMethod = "aes-128-gcm"
	AES256GCM        ShadowsocksMethod = "aes-256-gcm"
	Chacha20Poly1305 ShadowsocksMethod = "chacha20-ietf-poly1305"
)

// Settings is the per-protocol settings payload stored as proxies.settings
// jsonb. Exactly one of the typed fields is populated, matching which
// ProxyType this settings value belongs to.
type Settings struct {
	Type        ProxyType
	VMess       *VMessSettings
	VLESS       *VLESSSettings
	Trojan      *TrojanSettings
	Shadowsocks *ShadowsocksSettings
}

type VMessSettings struct {
	ID string `json:"id"`
}

type VLESSSettings struct {
	ID   string   `json:"id"`
	Flow XTLSFlow `json:"flow"`
}

type TrojanSettings struct {
	Password string   `json:"password"`
	Flow     XTLSFlow `json:"flow"`
}

type ShadowsocksSettings struct {
	Password string            `json:"password"`
	Method   ShadowsocksMethod `json:"method"`
}

// randomPassword mirrors app/utils/system.py's random_password:
// secrets.token_urlsafe(16) - 16 random bytes, unpadded base64url.
func randomPassword() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand failing means the OS entropy source is broken
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// hasSettings reports whether raw actually carries per-protocol settings to
// unmarshal - false for both "field omitted entirely" and an empty JSON
// array ("[]", allowing surrounding whitespace). That second shape is not
// hypothetical: a real, unmodified external reseller bot (confirmed live)
// builds its add-user request body by
// json_decode()-ing a stored plan template with PHP's associative-array
// mode, then json_encode()-ing it back - a round trip that cannot tell an
// empty JSON *object* ("{}", "no special settings for this protocol") from
// an empty JSON *array*, and always re-emits the latter. A
// Python/Pydantic stack coerces both the same way; this codebase's strict
// encoding/json unmarshal into a struct does not, and would otherwise
// reject a common, legitimate request shape with a 422 that has nothing to
// do with the request actually being wrong.
func hasSettings(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	return !bytes.Equal(bytes.TrimSpace(raw), []byte("[]"))
}

// FromWire builds a Settings from a client-supplied JSON payload for the
// given protocol, applying the same defaulting rules as the Python
// ProxySettings subclasses: a missing/empty id or password is generated,
// never left blank.
func FromWire(proxyType ProxyType, raw json.RawMessage) (Settings, error) {
	return parse(proxyType, raw, true)
}

// parse does the work for both entry points. coerceVisionFlow separates
// them: it belongs on the INPUT path only.
func parse(proxyType ProxyType, raw json.RawMessage, coerceVisionFlow bool) (Settings, error) {
	switch proxyType {
	case VMess:
		var s VMessSettings
		if hasSettings(raw) {
			if err := json.Unmarshal(raw, &s); err != nil {
				return Settings{}, fmt.Errorf("proxysettings: invalid vmess settings: %w", err)
			}
		}
		if s.ID == "" {
			s.ID = uuid.NewString()
		}
		return Settings{Type: VMess, VMess: &s}, nil

	case VLESS:
		var s VLESSSettings
		if hasSettings(raw) {
			if err := json.Unmarshal(raw, &s); err != nil {
				return Settings{}, fmt.Errorf("proxysettings: invalid vless settings: %w", err)
			}
		}
		if s.ID == "" {
			s.ID = uuid.NewString()
		}
		// Every VLESS config on this panel runs XTLS Vision - an explicit
		// empty/none flow from the client is coerced rather than stored as
		// sent, mirroring VLESSSettings.default_to_vision. See that
		// validator's comment in app/models/proxy.py for why this matters:
		// a client that copies the old dashboard's payload sends an
		// explicit empty flow on every update, which would otherwise
		// silently downgrade users away from Vision.
		if coerceVisionFlow && s.Flow == FlowNone {
			s.Flow = FlowVision
		}
		return Settings{Type: VLESS, VLESS: &s}, nil

	case Trojan:
		var s TrojanSettings
		if hasSettings(raw) {
			if err := json.Unmarshal(raw, &s); err != nil {
				return Settings{}, fmt.Errorf("proxysettings: invalid trojan settings: %w", err)
			}
		}
		if s.Password == "" {
			s.Password = randomPassword()
		}
		return Settings{Type: Trojan, Trojan: &s}, nil

	case Shadowsocks:
		var s ShadowsocksSettings
		if hasSettings(raw) {
			if err := json.Unmarshal(raw, &s); err != nil {
				return Settings{}, fmt.Errorf("proxysettings: invalid shadowsocks settings: %w", err)
			}
		}
		if s.Password == "" {
			s.Password = randomPassword()
		}
		if s.Method == "" {
			s.Method = Chacha20Poly1305
		}
		return Settings{Type: Shadowsocks, Shadowsocks: &s}, nil

	default:
		return Settings{}, fmt.Errorf("proxysettings: unknown proxy type %q", proxyType)
	}
}

// FromStored parses a Settings back out of the JSON already persisted in
// proxies.settings - no defaulting, the row is assumed already valid.
// FromStored reads what the database actually holds, WITHOUT the Vision
// coercion above. That distinction is load-bearing: the coercion exists so
// a client echoing back an empty flow cannot silently downgrade a Vision
// user, which is about writes. Applying it to reads instead told every node
// that users stored with no flow at all use Vision - and on a panel that
// genuinely runs plain VLESS, sing-box then rejected every single client
// with "flow mismatch: expected xtls-rprx-vision, but got none". Found on a
// real migrated panel whose 1,563 users were all stored with flow "".
func FromStored(proxyType ProxyType, raw []byte) (Settings, error) {
	return parse(ProxyType(proxyType), raw, false)
}

// MarshalJSON emits just the underlying typed struct (matching Python's
// settings.dict(no_obj=True) - no wrapper, no "type" field).
func (s Settings) MarshalJSON() ([]byte, error) {
	switch s.Type {
	case VMess:
		return json.Marshal(s.VMess)
	case VLESS:
		return json.Marshal(s.VLESS)
	case Trojan:
		return json.Marshal(s.Trojan)
	case Shadowsocks:
		return json.Marshal(s.Shadowsocks)
	default:
		return nil, fmt.Errorf("proxysettings: unknown proxy type %q", s.Type)
	}
}

// Revoke rotates the secret in place - a new UUID for vmess/vless, a new
// random password for trojan/shadowsocks - matching each Python
// ProxySettings subclass's revoke() method.
func (s *Settings) Revoke() {
	switch s.Type {
	case VMess:
		s.VMess.ID = uuid.NewString()
	case VLESS:
		s.VLESS.ID = uuid.NewString()
	case Trojan:
		s.Trojan.Password = randomPassword()
	case Shadowsocks:
		s.Shadowsocks.Password = randomPassword()
	}
}
