package subscription

import (
	"encoding/hex"
	"testing"
	"time"
)

func TestCreateTokenValidateTokenRoundTrip(t *testing.T) {
	secret := []byte("s3cr3t")
	token := CreateToken("alice", secret)
	username, _, ok := ValidateToken(token, secret)
	if !ok {
		t.Fatal("ValidateToken returned ok=false for a freshly-created token")
	}
	if username != "alice" {
		t.Errorf("username = %q, want %q", username, "alice")
	}
}

func TestCreateTokenRejectsTamperedSignature(t *testing.T) {
	secret := []byte("s3cr3t")
	token := CreateToken("alice", secret)
	tampered := token[:len(token)-1] + "x"
	if _, _, ok := ValidateToken(tampered, secret); ok {
		t.Error("ValidateToken accepted a token with a tampered signature")
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
	_, tokenTime, ok := ValidateToken(token, secret)
	if !ok {
		t.Fatal("ValidateToken returned ok=false")
	}

	if justBefore.After(tokenTime) {
		t.Errorf("token timestamp %v precedes a creation instant %v from the same call - "+
			"this reproduces the live 404-on-fresh-subscription bug", tokenTime, justBefore)
	}
}

// TestValidateTokenAcceptsARealPythonIssuedToken is a regression test for a
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
func TestValidateTokenAcceptsARealPythonIssuedToken(t *testing.T) {
	secret, err := hex.DecodeString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	// Computed independently via Python's real formula:
	// data = "testuser,1700000000"; data_b64 = urlsafe_b64(data).rstrip("=")
	// sig = urlsafe_b64(sha256((data_b64 + secret_hex).encode()))[:10]
	const pythonStyleToken = "dGVzdHVzZXIsMTcwMDAwMDAwMAl2sC7AkTtX"

	username, createdAt, ok := ValidateToken(pythonStyleToken, secret)
	if !ok {
		t.Fatal("ValidateToken rejected a validly-signed, Python-formula token")
	}
	if username != "testuser" {
		t.Errorf("username = %q, want %q", username, "testuser")
	}
	if createdAt.Unix() != 1700000000 {
		t.Errorf("createdAt = %v, want unix 1700000000", createdAt)
	}
}
