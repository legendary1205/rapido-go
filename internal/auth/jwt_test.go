package auth

import (
	"testing"
	"time"
)

func TestIssueAndVerify(t *testing.T) {
	issuer := NewTokenIssuer([]byte("test-secret"), time.Hour)

	token, err := issuer.Issue("alice", false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q, want %q", claims.Username, "alice")
	}
	if claims.IsSudo() {
		t.Error("IsSudo() = true, want false")
	}
}

func TestIssueSudoAccessClaim(t *testing.T) {
	issuer := NewTokenIssuer([]byte("test-secret"), time.Hour)

	token, err := issuer.Issue("root", true)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !claims.IsSudo() {
		t.Error("IsSudo() = false, want true")
	}
	if claims.Access != AccessSudo {
		t.Errorf("Access = %q, want %q", claims.Access, AccessSudo)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	issuer := NewTokenIssuer([]byte("secret-a"), time.Hour)
	other := NewTokenIssuer([]byte("secret-b"), time.Hour)

	token, err := issuer.Issue("alice", false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := other.Verify(token); err == nil {
		t.Error("Verify with wrong secret succeeded, want error")
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	issuer := NewTokenIssuer([]byte("test-secret"), time.Millisecond)

	token, err := issuer.Issue("alice", false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := issuer.Verify(token); err == nil {
		t.Error("Verify of expired token succeeded, want error")
	}
}

func TestIssueNoExpiryWhenTTLNonPositive(t *testing.T) {
	// Mirrors JWT_ACCESS_TOKEN_EXPIRE_MINUTES <= 0 in the current Python
	// implementation: the token carries no "exp" claim at all and never
	// expires on its own.
	issuer := NewTokenIssuer([]byte("test-secret"), 0)

	token, err := issuer.Issue("alice", false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil", claims.ExpiresAt)
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	issuer := NewTokenIssuer([]byte("test-secret"), time.Hour)
	if _, err := issuer.Verify("not-a-jwt"); err == nil {
		t.Error("Verify of garbage token succeeded, want error")
	}
}
