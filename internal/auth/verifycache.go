package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"
)

const (
	// verifyHitTTL: how long a correct (username, password) pair is trusted
	// without re-running bcrypt. Reseller bots log in before nearly every API
	// call, so this turns almost all of those into a map lookup.
	verifyHitTTL = 5 * time.Minute
	// verifyMissTTL: how long a wrong pair is remembered. Short, and keyed on
	// the exact password, so a person correcting a typo still gets a real
	// check while a misconfigured client repeating one bad password stops
	// costing a bcrypt (~280 ms of CPU) per attempt.
	verifyMissTTL = time.Minute
	// verifyMaxEntries bounds memory; when full, expired entries go first and
	// then an arbitrary one, which only ever costs someone a fresh bcrypt.
	verifyMaxEntries = 4096
)

type verifyEntry struct {
	ok      bool
	expires time.Time
}

// VerifyCache memoizes VerifyPassword verdicts. The key is an HMAC (random
// per-process key) over username, password and the stored hash, so the
// password itself is never kept, a changed password or hash misses by
// construction, and the keys are useless outside this process.
type VerifyCache struct {
	verify func(plain, hashed string) bool
	now    func() time.Time
	secret [32]byte

	mu      sync.Mutex
	entries map[[sha256.Size]byte]verifyEntry
}

func NewVerifyCache() *VerifyCache {
	return NewVerifyCacheWith(VerifyPassword)
}

// NewVerifyCacheWith lets tests count or fake the underlying comparison.
func NewVerifyCacheWith(verify func(plain, hashed string) bool) *VerifyCache {
	c := &VerifyCache{verify: verify, now: time.Now, entries: map[[sha256.Size]byte]verifyEntry{}}
	rand.Read(c.secret[:]) // never returns an error since Go 1.24
	return c
}

func (c *VerifyCache) key(username, plain, hashed string) [sha256.Size]byte {
	m := hmac.New(sha256.New, c.secret[:])
	// Length-prefixed so ("a", "b\x00c") and ("a\x00b", "c") cannot collide.
	var n [4]byte
	for _, s := range [...]string{username, plain, hashed} {
		binary.BigEndian.PutUint32(n[:], uint32(len(s)))
		m.Write(n[:])
		m.Write([]byte(s))
	}
	var k [sha256.Size]byte
	copy(k[:], m.Sum(nil))
	return k
}

// Verify reports whether plain matches hashed, running bcrypt only when this
// exact (username, plain, hashed) triple has no unexpired verdict.
func (c *VerifyCache) Verify(username, plain, hashed string) bool {
	k := c.key(username, plain, hashed)
	now := c.now()

	c.mu.Lock()
	if e, ok := c.entries[k]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.ok
	}
	c.mu.Unlock()

	ok := c.verify(plain, hashed)
	ttl := verifyMissTTL
	if ok {
		ttl = verifyHitTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= verifyMaxEntries {
		c.evictLocked(now)
	}
	c.entries[k] = verifyEntry{ok: ok, expires: now.Add(ttl)}
	return ok
}

func (c *VerifyCache) evictLocked(now time.Time) {
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) < verifyMaxEntries {
		return
	}
	for k := range c.entries {
		delete(c.entries, k)
		break
	}
}
