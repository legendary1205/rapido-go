package subscription

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"
)

func TestCreateTokenParseTokenRoundTrip(t *testing.T) {
	secret := []byte("s3cr3t")
	token := CreateToken("alice", secret)
	claims, ok := ParseToken(token, secret)
	if !ok {
		t.Fatal("ParseToken returned ok=false for a freshly-created token")
	}
	if claims.ByID || claims.Username != "alice" {
		t.Errorf("claims = %+v, want username %q", claims, "alice")
	}
}

func TestCreateTokenRejectsTamperedSignature(t *testing.T) {
	secret := []byte("s3cr3t")
	token := CreateToken("alice", secret)
	tampered := token[:len(token)-1] + "x"
	if _, ok := ParseToken(tampered, secret); ok {
		t.Error("ParseToken accepted a token with a tampered signature")
	}
}

// TestCreateTokenTimestampNeverPrecedesCreation guards against the exact bug
// this test caught live on the test server: a subscription token minted a
// few sub-second milliseconds after a user's Postgres created_at must never
// decode to a time strictly before that instant, or handleGetSubscription's
// `user.CreatedAt.After(tokenTime)` check permanently 404s a subscription
// URL that was valid the moment it was issued. Python's original rounds up
// (ceil(time.time())) for exactly this reason - CreateToken must too.
func TestCreateTokenTimestampNeverPrecedesCreation(t *testing.T) {
	secret := []byte("s3cr3t")
	// Simulate a Postgres created_at stamped a few milliseconds before the
	// token is minted, well within the same wall-clock second.
	justBefore := time.Now().UTC()

	token := CreateToken("alice", secret)
	claims, ok := ParseToken(token, secret)
	if !ok {
		t.Fatal("ParseToken returned ok=false")
	}

	if tokenTime := claims.CreatedAt; justBefore.After(tokenTime) {
		t.Errorf("token timestamp %v precedes a creation instant %v from the same call - "+
			"this reproduces the live 404-on-fresh-subscription bug", tokenTime, justBefore)
	}
}

// TestParseTokenAcceptsARealPythonIssuedToken is a regression test for a
// real production incident: sign() hashed the secret as its raw decoded
// bytes, but Python's create_subscription_token/get_subscription_payload
// (app/utils/jwt.py) hash it as its 64-character HEX STRING, concatenated
// as literal text - `(data_b64_str + get_secret_key()).encode("utf-8")`,
// where get_secret_key() is the jwt/jwt_secrets table's secret_key column
// verbatim. The two conventions hash completely different byte sequences
// (32 raw bytes vs. 64 ASCII hex-digit bytes) and were never compared
// against each other until a real cross-system migration needed a real,
// already-issued Python subscription link to keep validating on this Go
// binary - every single one of them failed, indistinguishable from a wrong
// secret value even after the secret's VALUE was correctly carried over.
//
// The token/secret pair below is synthetic (computed independently in
// Node from Python's own formula, using a throwaway secret - never a real
// installation's actual key), but the bug it caught was found against a
// real customer's real already-installed subscription link during a real
// migration: this reproduces the exact same "sign() disagrees with
// Python" defect with the same fixed, reviewable inputs instead of a
// production secret that has no business living in version control.
func TestParseTokenAcceptsARealPythonIssuedToken(t *testing.T) {
	secret, err := hex.DecodeString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	// Computed independently via Python's real formula:
	// data = "testuser,1700000000"; data_b64 = urlsafe_b64(data).rstrip("=")
	// sig = urlsafe_b64(sha256((data_b64 + secret_hex).encode()))[:10]
	const pythonStyleToken = "dGVzdHVzZXIsMTcwMDAwMDAwMAl2sC7AkTtX"

	claims, ok := ParseToken(pythonStyleToken, secret)
	if !ok {
		t.Fatal("ParseToken rejected a validly-signed, Python-formula token")
	}
	if claims.Username != "testuser" {
		t.Errorf("username = %q, want %q", claims.Username, "testuser")
	}
	if claims.CreatedAt.Unix() != 1700000000 {
		t.Errorf("createdAt = %v, want unix 1700000000", claims.CreatedAt)
	}
}

// pasarGuardTestSecret is a throwaway key in the shape every Marzban-lineage
// panel stores (64 lowercase hex characters); the tokens below were computed
// with PasarGuard 5.3.0's own create_subscription_token/get_subscription_payload
// formulas (app/utils/jwt.py, run verbatim in Python) against it - never a
// real installation's key.
func pasarGuardTestSecret(t *testing.T) []byte {
	t.Helper()
	secret, err := hex.DecodeString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return secret
}

// TestParseTokenAcceptsEveryPasarGuardFormat covers each link shape a
// customer migrated from PasarGuard may still hold. The v3 HMAC form is the
// one that matters most - it is what every PasarGuard 5.x panel issues today,
// and it names the user by id, not username.
func TestParseTokenAcceptsEveryPasarGuardFormat(t *testing.T) {
	secret := pasarGuardTestSecret(t)
	cases := []struct {
		name  string
		token string
		want  TokenClaims
	}{
		{"v3 hmac (current PasarGuard)", "djMsMTA1OSwxNzkwMTAzOTU0.N2eaiYAuhlIDoQnTj73GgF5rvh-q5IcZsXqdcs2WglE",
			TokenClaims{ByID: true, UserID: 1059, CreatedAt: time.Unix(1790103954, 0).UTC()}},
		{"hmac around a username payload", "YWxpY2UsMTc5MDEwMzk1NA.JCC5a3ekiJVNyNzXGXy7A4oMzB0ya56PMKb-rU_fBg8",
			TokenClaims{Username: "alice", CreatedAt: time.Unix(1790103954, 0).UTC()}},
		{"v2 with base64 signature", "djIsNDIsMTcwMDAwMDAwMAKEepvJ0wR6",
			TokenClaims{ByID: true, UserID: 42, CreatedAt: time.Unix(1700000000, 0).UTC()}},
		{"v2 with hex signature", "djIsNDIsMTcwMDAwMDAwMA2847a9bc9d",
			TokenClaims{ByID: true, UserID: 42, CreatedAt: time.Unix(1700000000, 0).UTC()}},
		{"username with hex signature", "dGVzdHVzZXIsMTcwMDAwMDAwMA976b02ec09",
			TokenClaims{Username: "testuser", CreatedAt: time.Unix(1700000000, 0).UTC()}},
		{"legacy JWT", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0ZXN0dXNlciIsImFjY2VzcyI6InN1YnNjcmlwdGlvbiIsImlhdCI6MTcwMDAwMDAwMH0.2CNh5Aqs7OEgn_URwc7fOcxXEWPPmr1433ugPESuCWE",
			TokenClaims{Username: "testuser", CreatedAt: time.Unix(1700000000, 0).UTC()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseToken(tc.token, secret)
			if !ok {
				t.Fatalf("ParseToken rejected %q", tc.token)
			}
			if got != tc.want {
				t.Errorf("claims = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseTokenRejectsForgedPasarGuardTokens(t *testing.T) {
	secret := pasarGuardTestSecret(t)
	cases := map[string]string{
		// The right payload signed with a different key.
		"v3 signed by another key": "djMsMTA1OSwxNzkwMTAzOTU0.-WGHSHfwoDv6BWaIquM30q7FU104wmtn66IlXhcFcXs",
		// A valid signature moved onto a different payload ("v3,1,1790103954").
		"v3 payload swapped": "djMsMSwxNzkwMTAzOTU0.N2eaiYAuhlIDoQnTj73GgF5rvh-q5IcZsXqdcs2WglE",
		"v3 signature truncated": "djMsMTA1OSwxNzkwMTAzOTU0.N2eaiYAuhlIDoQnTj73GgF5rvh-q5IcZsXqdcs2Wgl",
		"v2 hex signature altered": "djIsNDIsMTcwMDAwMDAwMA2847a9bc9e",
		// A correctly signed JWT for the admin API, not a subscription.
		"admin JWT": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0ZXN0dXNlciIsImFjY2VzcyI6ImFkbWluIiwiaWF0IjoxNzAwMDAwMDAwfQ.zS-zHfBfNMdT_k0lDUM8Xsc8WlQGo62MRy4yiWOClGM",
		"too short": "djMsMTA1OSw",
	}
	for name, token := range cases {
		if got, ok := ParseToken(token, secret); ok {
			t.Errorf("%s: ParseToken accepted %q as %+v", name, token, got)
		}
	}
}

// TestParseTokenRejectsMalformedV3Payloads: a v2/v3 payload must carry a
// numeric id and timestamp - a correctly signed but garbled one is refused,
// exactly as _parse_subscription_data refuses it. ("v3,42" is NOT in this
// list: it is a valid username payload naming a user called "v3", in both
// panels.)
func TestParseTokenRejectsMalformedV3Payloads(t *testing.T) {
	for _, raw := range []string{"v3,abc,1700000000", "v3,42,soon", "v4,42,1700000000", "a,b,c,d", "lonely"} {
		if got, ok := parseTokenPayload(base64.RawURLEncoding.EncodeToString([]byte(raw))); ok {
			t.Errorf("payload %q accepted as %+v", raw, got)
		}
	}
}
