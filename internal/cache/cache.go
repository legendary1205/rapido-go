// Package cache wraps the shared Redis client and the key-naming convention
// used across the panel. Because every process (api and backend roles alike)
// reads the same Redis instance directly, there is only ever one copy of
// this data - unlike the old per-process DictStorage + NATS broadcast
// pattern, nothing here needs a separate invalidation message.
package cache

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	rdb    *redis.Client
	logger *slog.Logger
}

func New(addr, password string, db int, logger *slog.Logger) *Client {
	return &Client{
		rdb: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		}),
		logger: logger,
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

	// Cache-aside namespaces for the Postgres reads on this project's
	// hottest paths (admin resolution on nearly every request, the
	// subscription-generation N+1) - see internal/httpapi/store.go for the
	// Cached*/Invalidate* methods built on these. Deliberately separate
	// names from nsHost/HostKey above: that one is the *node* metrics
	// cache (a different table entirely), and colliding the naming with
	// the VPN "hosts" table here would be a real bug waiting to happen.
	nsAdminByUsername    = "rapido:admin:by_username"
	nsAdminByID          = "rapido:admin:by_id"
	nsInbound            = "rapido:inbound"
	nsInboundTagsByProto = "rapido:inbound_tags"
	nsInboundHosts       = "rapido:inbound_hosts"
	nsExcludedInbounds   = "rapido:excluded_inbounds"
)

func AdminByUsernameKey(username string) string {
	return fmt.Sprintf("%s:%s", nsAdminByUsername, username)
}
func AdminByIDKey(id int32) string      { return fmt.Sprintf("%s:%d", nsAdminByID, id) }
func InboundByTagKey(tag string) string { return fmt.Sprintf("%s:%s", nsInbound, tag) }
func InboundTagsByProtocolKey(protocol string) string {
	return fmt.Sprintf("%s:%s", nsInboundTagsByProto, protocol)
}
func InboundHostsKey(tag string) string { return fmt.Sprintf("%s:%s", nsInboundHosts, tag) }
func ExcludedInboundTagsKey(proxyID int32) string {
	return fmt.Sprintf("%s:%d", nsExcludedInbounds, proxyID)
}

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
