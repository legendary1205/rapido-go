package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// GetOrSet implements cache-aside generically: a hit deserializes into T; a
// miss, a corrupt entry, or any Redis error falls through to fetch (the
// real Postgres call) and best-effort repopulates the cache. Redis being
// slow or unreachable degrades this to exactly today's uncached behavior
// for that one call rather than failing the request - Redis has never been
// on the critical path before this, and this helper keeps it that way.
//
// fetch's error (including a real "not found" like pgx.ErrNoRows) always
// propagates unchanged and is never cached - callers that branch on a
// specific error (errors.Is(err, pgx.ErrNoRows)) keep working with no
// change to their own logic.
func GetOrSet[T any](ctx context.Context, c *Client, key string, ttl time.Duration, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	if raw, err := c.Get(ctx, key); err == nil {
		var v T
		if json.Unmarshal([]byte(raw), &v) == nil {
			return v, nil
		}
	} else if !errors.Is(err, ErrNil) {
		c.logger.Warn("cache get failed, falling back to source of truth", "key", key, "error", err)
	}

	v, err := fetch(ctx)
	if err != nil {
		return zero, err
	}
	if raw, mErr := json.Marshal(v); mErr == nil {
		if sErr := c.Set(ctx, key, string(raw), ttl); sErr != nil {
			c.logger.Warn("cache set failed", "key", key, "error", sErr)
		}
	}
	return v, nil
}

// GetOrSetString is GetOrSet for a value that is ALREADY the exact string to
// be served - no JSON round-trip in either direction.
//
// GetOrSet is the right shape when the caller needs a typed value it will go
// on to inspect. It is the wrong shape when the caller only ever writes the
// value to a response, because then a cache hit pays a full decode into Go
// structs followed immediately by a full re-encode - work that exists purely
// to undo itself. On the node-config body (~14 MB, re-pulled by every node
// every few seconds) that round-trip was the panel's single largest CPU
// cost. Everything else here is deliberately identical to GetOrSet: Redis
// errors degrade to an uncached fetch rather than failing the request, and
// fetch's own error propagates unchanged and is never cached.
func GetOrSetString(ctx context.Context, c *Client, key string, ttl time.Duration, fetch func(context.Context) (string, error)) (string, error) {
	if raw, err := c.Get(ctx, key); err == nil {
		return raw, nil
	} else if !errors.Is(err, ErrNil) {
		c.logger.Warn("cache get failed, falling back to source of truth", "key", key, "error", err)
	}

	v, err := fetch(ctx)
	if err != nil {
		return "", err
	}
	if sErr := c.Set(ctx, key, v, ttl); sErr != nil {
		c.logger.Warn("cache set failed", "key", key, "error", sErr)
	}
	return v, nil
}
