package subscription

import (
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
