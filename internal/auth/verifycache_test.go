package auth

import (
	"testing"
	"time"
)

// countingVerifier stands in for bcrypt: it accepts exactly one password and
// counts how often it was actually consulted.
type countingVerifier struct {
	calls   int
	correct string
}

func (v *countingVerifier) verify(plain, hashed string) bool {
	v.calls++
	return plain == v.correct
}

func newTestCache(v *countingVerifier) (*VerifyCache, *time.Time) {
	c := NewVerifyCacheWith(v.verify)
	clock := time.Unix(1_000_000, 0)
	c.now = func() time.Time { return clock }
	return c, &clock
}

func TestVerifyCacheHitSkipsBcrypt(t *testing.T) {
	v := &countingVerifier{correct: "right"}
	c, clock := newTestCache(v)

	for i := 0; i < 3; i++ {
		if !c.Verify("alice", "right", "hash1") {
			t.Fatalf("attempt %d rejected the correct password", i)
		}
	}
	if v.calls != 1 {
		t.Errorf("verifier called %d times for 3 identical correct logins, want 1", v.calls)
	}

	*clock = clock.Add(verifyHitTTL + time.Second)
	if !c.Verify("alice", "right", "hash1") {
		t.Fatal("correct password rejected after the entry expired")
	}
	if v.calls != 2 {
		t.Errorf("verifier called %d times after the 5 minute TTL, want 2", v.calls)
	}
}

func TestVerifyCacheChangedHashOrPasswordMisses(t *testing.T) {
	v := &countingVerifier{correct: "right"}
	c, _ := newTestCache(v)

	c.Verify("alice", "right", "hash1")
	// A password change stores a new hash: the cached "right" must not carry over.
	c.Verify("alice", "right", "hash2")
	if v.calls != 2 {
		t.Errorf("verifier called %d times, want 2 (a new stored hash must miss)", v.calls)
	}
	// A different username with the same password and hash is a different key.
	c.Verify("bob", "right", "hash1")
	if v.calls != 3 {
		t.Errorf("verifier called %d times, want 3 (a different username must miss)", v.calls)
	}
	// A wrong password is never answered from the correct password's entry.
	if c.Verify("alice", "wrong", "hash1") {
		t.Error("wrong password accepted after the right one was cached")
	}
	if v.calls != 4 {
		t.Errorf("verifier called %d times, want 4 (a different password must miss)", v.calls)
	}
}

func TestVerifyCacheNegativeEntryExpiresAfterAMinute(t *testing.T) {
	v := &countingVerifier{correct: "right"}
	c, clock := newTestCache(v)

	for i := 0; i < 5; i++ {
		if c.Verify("alice", "wrong", "hash1") {
			t.Fatal("wrong password accepted")
		}
	}
	if v.calls != 1 {
		t.Errorf("verifier called %d times for 5 identical wrong logins, want 1", v.calls)
	}

	// A different wrong password, and the right one, still get a real check.
	c.Verify("alice", "typo", "hash1")
	if !c.Verify("alice", "right", "hash1") {
		t.Error("correct password rejected after a wrong one was cached")
	}
	if v.calls != 3 {
		t.Errorf("verifier called %d times, want 3", v.calls)
	}

	*clock = clock.Add(verifyMissTTL + time.Second)
	c.Verify("alice", "wrong", "hash1")
	if v.calls != 4 {
		t.Errorf("verifier called %d times after the negative TTL, want 4", v.calls)
	}
}

func TestVerifyCacheKeysAreLengthPrefixed(t *testing.T) {
	v := &countingVerifier{correct: "b\x00c"}
	c, _ := newTestCache(v)
	if !c.Verify("a", "b\x00c", "h") {
		t.Fatal("setup: correct password rejected")
	}
	// Same concatenation, different split: must not reuse the entry above.
	if c.Verify("a\x00b", "c", "h") {
		t.Error("a username/password split collided with a cached correct login")
	}
}

func TestVerifyCacheIsSizeBounded(t *testing.T) {
	v := &countingVerifier{correct: "right"}
	c, _ := newTestCache(v)
	for i := 0; i < verifyMaxEntries*2; i++ {
		c.Verify("alice", string(rune('a'+i%26))+time.Duration(i).String(), "hash1")
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > verifyMaxEntries {
		t.Errorf("cache holds %d entries, want at most %d", n, verifyMaxEntries)
	}
}

func TestVerifyCacheWithRealBcrypt(t *testing.T) {
	hashed, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	c := NewVerifyCache()
	if !c.Verify("alice", "s3cret", hashed) || !c.Verify("alice", "s3cret", hashed) {
		t.Error("correct password rejected")
	}
	if c.Verify("alice", "nope", hashed) || c.Verify("alice", "nope", hashed) {
		t.Error("wrong password accepted")
	}
}
