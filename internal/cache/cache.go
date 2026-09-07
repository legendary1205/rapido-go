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

	// nsNodeConfig caches the fully-computed node-config payload (see
	// internal/httpapi/nodeconfig.go) - inbounds + their active users +
	// core_config's outbounds/routing/dns - not the raw core_config row
	// nsCoreConfig above caches. Every node in the fleet is served the
	// identical payload (same architecture as the current Python system's
	// single shared config), so this is one global key, not per-node.
	nsNodeConfig = "rapido:node_config"

	// nsMaintenance gates the whole panel during a destructive database
	// restore (internal/httpapi/backup.go) - set for the duration of a
	// schema drop+recreate so the HTTP middleware can reject requests and
	// the backend-singleton's background loops (reviewjob, hostmetrics) can
	// skip a tick, instead of either racing a query against a half-dropped
	// schema. A Redis key rather than a DB row/column, since the database
	// itself is what's being wiped.
	nsMaintenance = "rapido:maintenance"
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

func NodeConfigKey() string { return nsNodeConfig }

func (c *Client) Get(ctx context.Context, key string) (string, error) {
	return c.rdb.Get(ctx, key).Result()
}

func (c *Client) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

func (c *Client) Del(ctx context.Context, keys ...string) error {
	return c.rdb.Del(ctx, keys...).Err()
}

// FlushAll wipes the entire Redis logical DB this client is connected to.
// Safe here specifically because this project's Redis instance is
// dedicated to this panel (docker-compose.yml runs it just for rapido-go,
// nothing else shares the connection/DB index) - a per-namespace scan+del
// would be more surgical but strictly unnecessary, and this is only ever
// called right after a full database restore, when every cached value is
// definitionally stale anyway.
func (c *Client) FlushAll(ctx context.Context) error {
	return c.rdb.FlushDB(ctx).Err()
}

// SetMaintenanceMode toggles the panel-wide maintenance flag (see
// nsMaintenance's doc comment). No TTL: cleared explicitly by the restore
// handler's defer, not left to expire on its own - an expiring flag could
// lapse mid-restore under a slow disk/large dump.
func (c *Client) SetMaintenanceMode(ctx context.Context, on bool) error {
	if !on {
		return c.rdb.Del(ctx, nsMaintenance).Err()
	}
	return c.rdb.Set(ctx, nsMaintenance, "1", 0).Err()
}

// IsMaintenanceMode reports whether a restore is currently in progress.
func (c *Client) IsMaintenanceMode(ctx context.Context) (bool, error) {
	n, err := c.rdb.Exists(ctx, nsMaintenance).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ErrNil is returned by Get when the key does not exist - a re-export of
// redis.Nil so callers don't need to import go-redis directly just to check
// for a cache miss.
var ErrNil = redis.Nil
