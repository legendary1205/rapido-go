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
	nsExcludedInbounds   = "rapido:excluded_inbounds"

	// nsNodeConfig is the key earlier releases cached one fleet-wide
	// node-config payload under. Payloads are now per node profile and held
	// in process memory, validated against a data version (see
	// internal/httpapi/nodeconfigcache.go); the key is only still deleted so
	// a process from an older release stops serving what it stored.
	nsNodeConfig = "rapido:node_config"

	// nsMaintenance gates the whole panel during a destructive database
	// restore (internal/httpapi/backup.go) - set for the duration of a
	// schema drop+recreate so the HTTP middleware can reject requests and
	// the backend-singleton's background loops (reviewjob, hostmetrics) can
	// skip a tick, instead of either racing a query against a half-dropped
	// schema. A Redis key rather than a DB row/column, since the database
	// itself is what's being wiped.
	nsMaintenance = "rapido:maintenance"

	// nsLoginAttempts backs a fixed-window per-IP rate limit on
	// POST /api/admin/token (see internal/httpapi/admin.go's handleLogin) -
	// neither this panel nor the Python original ever had one, a real gap
	// (login brute-force possible) rather than a deliberate scope cut.
	nsLoginAttempts = "rapido:login_attempts"

	// nsGatewayPeerStatus caches one peer's last-fetched GET .../gateway/status
	// response (crowdedness + real hosts) - see internal/gatewayjob, the
	// periodic BACKEND-only writer, and internal/httpapi/subscription.go's
	// forEachUserHost, the reader. Deliberately a plain TTL'd cache entry, not
	// invalidated on any write: nothing local ever changes a peer's own
	// crowdedness/hosts, only that peer's own next refresh does, and a peer
	// that stops refreshing (offline) should simply age out and disappear
	// from subscription merging once its TTL lapses, not linger forever.
	nsGatewayPeerStatus = "rapido:gateway_peer_status"
)

func AdminByUsernameKey(username string) string {
	return fmt.Sprintf("%s:%s", nsAdminByUsername, username)
}
func AdminByIDKey(id int32) string      { return fmt.Sprintf("%s:%d", nsAdminByID, id) }
func InboundByTagKey(tag string) string { return fmt.Sprintf("%s:%s", nsInbound, tag) }
func InboundTagsByProtocolKey(protocol string) string {
	return fmt.Sprintf("%s:%s", nsInboundTagsByProto, protocol)
}
func ExcludedInboundTagsKey(proxyID int32) string {
	return fmt.Sprintf("%s:%d", nsExcludedInbounds, proxyID)
}

func HostKey(nodeID string) string { return fmt.Sprintf("%s:%s", nsHost, nodeID) }

func InboundCountKey(tag string) string { return fmt.Sprintf("%s:%s", nsInboundCount, tag) }

func PortConnKey(port int) string { return fmt.Sprintf("%s:%d", nsPortConn, port) }

func LoginAttemptsKey(ip string) string { return fmt.Sprintf("%s:%s", nsLoginAttempts, ip) }

func GatewayPeerStatusKey(peerID int32) string {
	return fmt.Sprintf("%s:%d", nsGatewayPeerStatus, peerID)
}

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

// Incr implements a standard fixed-window counter: increments key by 1 and,
// only on the very first increment of a window (the count that just made it
// 1), sets its expiry to ttl - every later call in the same window just
// bumps the count without touching the TTL Redis already has running. The
// returned count is only meaningful up to the caller's own comparison
// threshold; there's no separate "reset" operation since the key simply
// expires on its own.
func (c *Client) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		if err := c.rdb.Expire(ctx, key, ttl).Err(); err != nil {
			return n, err
		}
	}
	return n, nil
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
