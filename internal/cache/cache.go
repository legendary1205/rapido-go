// Package cache wraps the shared Redis client and the key-naming convention
// used across the panel. Because every process (api and backend roles alike)
// reads the same Redis instance directly, there is only ever one copy of
// this data - unlike the old per-process DictStorage + NATS broadcast
// pattern, nothing here needs a separate invalidation message.
package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	rdb *redis.Client
}

func New(addr, password string, db int) *Client {
	return &Client{
		rdb: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		}),
	}
}

func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

func (c *Client) Close() error {
	return c.rdb.Close()
}

func (c *Client) Raw() *redis.Client {
	return c.rdb
}

// Key namespaces. Every cache key and stream name is built through one of
// these helpers so the naming convention lives in one place.
const (
	nsHost         = "rapido:host"
	nsInboundCount = "rapido:inbound_count"
	nsSettings     = "rapido:settings"
	nsPortConn     = "rapido:port_conn"
	nsCoreConfig   = "rapido:core_config"

	// StreamCommands carries fire-and-forget node/core commands from any api
	// process to the singleton backend process (replaces the NATS command
	// topic). StreamRPC carries request/response calls that need a reply
	// (replaces the NATS rpc_client/worker_service pattern).
	StreamCommands = "rapido:stream:commands"
	StreamRPC      = "rapido:stream:rpc"

	// AdvisoryLockBackendSingleton is the pg_try_advisory_lock key the
	// backend-role process holds for as long as it's the active singleton -
	// the Postgres equivalent of the current MySQL GET_LOCK() usage.
	AdvisoryLockBackendSingleton int64 = 0x52415049444f01 // "RAPIDO" + role tag, arbitrary but stable
)

func HostKey(nodeID string) string { return fmt.Sprintf("%s:%s", nsHost, nodeID) }

func InboundCountKey(tag string) string { return fmt.Sprintf("%s:%s", nsInboundCount, tag) }

func PortConnKey(port int) string { return fmt.Sprintf("%s:%d", nsPortConn, port) }

func SettingsKey() string { return nsSettings }

func CoreConfigKey() string { return nsCoreConfig }

func (c *Client) Get(ctx context.Context, key string) (string, error) {
	return c.rdb.Get(ctx, key).Result()
}

func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

func (c *Client) Del(ctx context.Context, keys ...string) error {
	return c.rdb.Del(ctx, keys...).Err()
}

// ErrNil is returned by Get when the key does not exist - a re-export of
// redis.Nil so callers don't need to import go-redis directly just to check
// for a cache miss.
var ErrNil = redis.Nil
