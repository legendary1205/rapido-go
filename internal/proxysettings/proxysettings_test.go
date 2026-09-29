package proxysettings

import (
	"encoding/json"
	"testing"
)

func TestFromWireGeneratesVMessID(t *testing.T) {
	s, err := FromWire(VMess, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.VMess.ID == "" {
		t.Error("VMess.ID is empty, want an auto-generated UUID")
	}
}

func TestFromWirePreservesGivenVMessID(t *testing.T) {
	s, err := FromWire(VMess, json.RawMessage(`{"id":"35e4e39c-7d5c-4f4b-8b71-558e4f37ff53"}`))
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.VMess.ID != "35e4e39c-7d5c-4f4b-8b71-558e4f37ff53" {
		t.Errorf("VMess.ID = %q, want the given id preserved", s.VMess.ID)
	}
}

func TestFromWireVLESSDefaultsFlowToVision(t *testing.T) {
	// Mirrors VLESSSettings.default_to_vision: a missing, empty, or
	// explicit "none" flow all coerce to Vision - every VLESS config on
	// this panel runs XTLS Vision.
	cases := []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		json.RawMessage(`{"flow":""}`),
	}
	for _, raw := range cases {
		s, err := FromWire(VLESS, raw)
		if err != nil {
			t.Fatalf("FromWire(%s): %v", raw, err)
		}
		if s.VLESS.Flow != FlowVision {
			t.Errorf("FromWire(%s).Flow = %q, want %q", raw, s.VLESS.Flow, FlowVision)
		}
	}
}

func TestFromWireVLESSGeneratesID(t *testing.T) {
	s, err := FromWire(VLESS, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.VLESS.ID == "" {
		t.Error("VLESS.ID is empty, want an auto-generated UUID")
	}
}

func TestFromWireTrojanGeneratesPassword(t *testing.T) {
	s, err := FromWire(Trojan, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.Trojan.Password == "" {
		t.Error("Trojan.Password is empty, want an auto-generated password")
	}
}

func TestFromWireShadowsocksDefaults(t *testing.T) {
	s, err := FromWire(Shadowsocks, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.Shadowsocks.Password == "" {
		t.Error("Shadowsocks.Password is empty, want an auto-generated password")
	}
	if s.Shadowsocks.Method != Chacha20Poly1305 {
		t.Errorf("Shadowsocks.Method = %q, want default %q", s.Shadowsocks.Method, Chacha20Poly1305)
	}
}

// TestFromWireTreatsEmptyArrayAsNoSettings covers a real, live-confirmed
// compatibility gap: a real, unmodified external reseller bot sends "[]" instead of "{}" for a protocol's settings
// once its plan template round-trips through PHP's associative-array JSON
// decode/encode (see hasSettings's own doc comment for the exact
// mechanism) - every protocol must accept this shape the same as no
// settings supplied at all, not reject it as malformed input.
func TestFromWireTreatsEmptyArrayAsNoSettings(t *testing.T) {
	for _, proxyType := range []ProxyType{VMess, VLESS, Trojan, Shadowsocks, Hysteria2, TUIC, Snell} {
		s, err := FromWire(proxyType, json.RawMessage(`[]`))
		if err != nil {
			t.Errorf("FromWire(%s, []): %v, want the same defaulting as no settings at all", proxyType, err)
		}
		switch proxyType {
		case VMess:
			if s.VMess.ID == "" {
				t.Error("VMess.ID is empty, want an auto-generated UUID")
			}
		case VLESS:
			if s.VLESS.ID == "" || s.VLESS.Flow != FlowVision {
				t.Errorf("VLESS = %+v, want a generated id and Vision flow", s.VLESS)
			}
		case Trojan:
			if s.Trojan.Password == "" {
				t.Error("Trojan.Password is empty, want an auto-generated password")
			}
		case Shadowsocks:
			if s.Shadowsocks.Password == "" || s.Shadowsocks.Method == "" {
				t.Errorf("Shadowsocks = %+v, want a generated password and default method", s.Shadowsocks)
			}
		case Hysteria2:
			if s.Hysteria2.Password == "" {
				t.Error("Hysteria2.Password is empty, want an auto-generated password")
			}
		case TUIC:
			if s.TUIC.ID == "" || s.TUIC.Password == "" {
				t.Errorf("TUIC = %+v, want a generated id and password", s.TUIC)
			}
		case Snell:
			if s.Snell.UserKey == "" {
				t.Error("Snell.UserKey is empty, want an auto-generated user key")
			}
		}
	}
}

func TestFromWireSnellGeneratesUserKey(t *testing.T) {
	s, err := FromWire(Snell, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.Snell.UserKey == "" {
		t.Error("Snell.UserKey is empty, want an auto-generated user key")
	}
}

func TestFromWireSnellPreservesGivenUserKey(t *testing.T) {
	s, err := FromWire(Snell, json.RawMessage(`{"user_key":"mine"}`))
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.Snell.UserKey != "mine" {
		t.Errorf("Snell.UserKey = %q, want the given key preserved", s.Snell.UserKey)
	}
}

func TestFromWireHysteria2GeneratesPassword(t *testing.T) {
	s, err := FromWire(Hysteria2, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.Hysteria2.Password == "" {
		t.Error("Hysteria2.Password is empty, want an auto-generated password")
	}
}

func TestFromWireHysteria2PreservesGivenPassword(t *testing.T) {
	s, err := FromWire(Hysteria2, json.RawMessage(`{"password":"mine"}`))
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.Hysteria2.Password != "mine" {
		t.Errorf("Hysteria2.Password = %q, want the given password preserved", s.Hysteria2.Password)
	}
}

func TestFromWireTUICGeneratesIDAndPassword(t *testing.T) {
	s, err := FromWire(TUIC, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.TUIC.ID == "" {
		t.Error("TUIC.ID is empty, want an auto-generated UUID")
	}
	if s.TUIC.Password == "" {
		t.Error("TUIC.Password is empty, want an auto-generated password")
	}
}

func TestFromWireTUICPreservesGivenIDAndPassword(t *testing.T) {
	s, err := FromWire(TUIC, json.RawMessage(`{"id":"8f8a4c1e-1e2a-4b8a-9b1a-0000000000aa","password":"mine"}`))
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if s.TUIC.ID != "8f8a4c1e-1e2a-4b8a-9b1a-0000000000aa" || s.TUIC.Password != "mine" {
		t.Errorf("TUIC = %+v, want both given values preserved", s.TUIC)
	}
}

// TestFromWireStillRejectsNonEmptyArray proves the compatibility shim is
// narrowly scoped to exactly "[]" - a genuinely malformed non-empty array
// must still be rejected, not silently swallowed.
func TestFromWireStillRejectsNonEmptyArray(t *testing.T) {
	_, err := FromWire(VLESS, json.RawMessage(`[1,2,3]`))
	if err == nil {
		t.Error("FromWire(VLESS, [1,2,3]) succeeded, want an error - this is not the empty-array compatibility shape")
	}
}

func TestFromWireRejectsUnknownType(t *testing.T) {
	if _, err := FromWire("wireguard", nil); err == nil {
		t.Error("FromWire with an unknown proxy type succeeded, want an error")
	}
}

func TestRevokeRotatesSecret(t *testing.T) {
	s, err := FromWire(VMess, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	before := s.VMess.ID
	s.Revoke()
	if s.VMess.ID == before {
		t.Error("Revoke() did not change the VMess id")
	}

	ts, err := FromWire(Trojan, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	beforePw := ts.Trojan.Password
	ts.Revoke()
	if ts.Trojan.Password == beforePw {
		t.Error("Revoke() did not change the Trojan password")
	}

	hs, err := FromWire(Hysteria2, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	beforeHy := hs.Hysteria2.Password
	hs.Revoke()
	if hs.Hysteria2.Password == beforeHy {
		t.Error("Revoke() did not change the Hysteria2 password")
	}

	tu, err := FromWire(TUIC, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	beforeID, beforeTuPw := tu.TUIC.ID, tu.TUIC.Password
	tu.Revoke()
	if tu.TUIC.ID == beforeID || tu.TUIC.Password == beforeTuPw {
		t.Errorf("Revoke() did not rotate both TUIC id and password: before (%s, %s), after (%s, %s)", beforeID, beforeTuPw, tu.TUIC.ID, tu.TUIC.Password)
	}

	sn, err := FromWire(Snell, nil)
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	beforeKey := sn.Snell.UserKey
	sn.Revoke()
	if sn.Snell.UserKey == beforeKey {
		t.Error("Revoke() did not change the Snell user key")
	}
}

func TestMarshalJSONEmitsBareStruct(t *testing.T) {
	// Matches Python's settings.dict(no_obj=True): just the typed fields,
	// no wrapper object and no "type" discriminator field.
	s, err := FromWire(VMess, json.RawMessage(`{"id":"35e4e39c-7d5c-4f4b-8b71-558e4f37ff53"}`))
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, hasType := decoded["type"]; hasType {
		t.Error("marshaled settings unexpectedly include a \"type\" field")
	}
	if decoded["id"] != "35e4e39c-7d5c-4f4b-8b71-558e4f37ff53" {
		t.Errorf("marshaled id = %v, want the original uuid", decoded["id"])
	}
}

// TestStoredFlowIsNotCoercedToVision pins the difference between the two
// entry points. The Vision coercion protects a WRITE (a client echoing an
// empty flow must not downgrade a Vision user); applying it to a READ told
// every node that a user stored with no flow uses Vision, and sing-box then
// refused every client on a plain-VLESS panel with "flow mismatch".
func TestStoredFlowIsNotCoercedToVision(t *testing.T) {
	const stored = `{"id":"8d00d821-cc52-4d99-9efe-cfc78fbc97b3","flow":""}`

	fromStored, err := FromStored(VLESS, []byte(stored))
	if err != nil {
		t.Fatalf("FromStored: %v", err)
	}
	if got := fromStored.VLESS.Flow; got != FlowNone {
		t.Errorf("FromStored flow = %q, want it left empty - the database is the truth on a read", got)
	}

	// The write path must still coerce, or an API client sending an explicit
	// empty flow silently downgrades a Vision user.
	fromWire, err := FromWire(VLESS, []byte(stored))
	if err != nil {
		t.Fatalf("FromWire: %v", err)
	}
	if got := fromWire.VLESS.Flow; got != FlowVision {
		t.Errorf("FromWire flow = %q, want %q", got, FlowVision)
	}

	// A stored Vision user must keep Vision on a read - that is the whole
	// fleet on the main panel, so this is the regression that would hurt.
	visionStored := `{"id":"8d00d821-cc52-4d99-9efe-cfc78fbc97b3","flow":"xtls-rprx-vision"}`
	keep, err := FromStored(VLESS, []byte(visionStored))
	if err != nil {
		t.Fatalf("FromStored(vision): %v", err)
	}
	if got := keep.VLESS.Flow; got != FlowVision {
		t.Errorf("stored vision flow = %q, want it preserved", got)
	}
}
