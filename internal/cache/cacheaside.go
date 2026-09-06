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
