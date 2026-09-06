package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/legendary1205/rapido-go/internal/cache"
	"github.com/legendary1205/rapido-go/internal/db/generated"
)

// Cache-aside TTLs, tiered by risk rather than uniform - see the plan's
// reasoning: admin is short because is_sudo/password_reset_at staleness is
// a security property, not just a performance one; the rest are safety
// nets behind explicit invalidation, not the primary correctness mechanism.
const (
	adminCacheTTL            = 15 * time.Minute
	inboundCacheTTL          = 6 * time.Hour
	excludedInboundsCacheTTL = 24 * time.Hour
	// integrationSettingsCacheTTL is a safety net only - the settings row is
	// a singleton that handleUpdateIntegrationSettings explicitly
	// invalidates on every write, same as every other Cached*/Invalidate*
	// pair here.
	integrationSettingsCacheTTL = 1 * time.Hour
)

type Store struct {
	Pool    *pgxpool.Pool
	Queries *generated.Queries
	Cache   *cache.Client
}

func NewStore(pool *pgxpool.Pool, cacheClient *cache.Client) *Store {
	return &Store{Pool: pool, Queries: generated.New(pool), Cache: cacheClient}
}

// ResolveAdmin implements auth.Resolver against the real admins table -
// cached, since this runs on nearly every authenticated request (see
// RequireAdmin/RequireSudo).
func (s *Store) ResolveAdmin(ctx context.Context, username string) (adminID int32, isSudo bool, passwordResetAt *time.Time, found bool, err error) {
	admin, err := s.CachedGetAdminByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil, false, nil
		}
		return 0, false, nil, false, err
	}
	if admin.PasswordResetAt.Valid {
		t := admin.PasswordResetAt.Time
		passwordResetAt = &t
	}
	return admin.ID, admin.IsSudo, passwordResetAt, true, nil
}

// CachedGetAdminByUsername and CachedGetAdminByID share one cache entry per
// admin, written under both keys together (see the CreateAdmin/UpdateAdmin
// write paths and InvalidateAdmin below) - usernames never change in this
// system (no rename endpoint exists), so the two keys can never drift
// against each other.
func (s *Store) CachedGetAdminByUsername(ctx context.Context, username string) (generated.Admin, error) {
	return cache.GetOrSet(ctx, s.Cache, cache.AdminByUsernameKey(username), adminCacheTTL, func(ctx context.Context) (generated.Admin, error) {
		return s.Queries.GetAdminByUsername(ctx, username)
	})
}

func (s *Store) CachedGetAdminByID(ctx context.Context, id int32) (generated.Admin, error) {
	return cache.GetOrSet(ctx, s.Cache, cache.AdminByIDKey(id), adminCacheTTL, func(ctx context.Context) (generated.Admin, error) {
		return s.Queries.GetAdminByID(ctx, id)
	})
}

// InvalidateAdmin busts both of an admin's cache entries - call after any
// write to that admin's row (UpdateAdmin, DeleteAdmin).
func (s *Store) InvalidateAdmin(ctx context.Context, id int32, username string) error {
	return s.Cache.Del(ctx, cache.AdminByIDKey(id), cache.AdminByUsernameKey(username))
}

func (s *Store) CachedGetInboundByTag(ctx context.Context, tag string) (generated.Inbound, error) {
	return cache.GetOrSet(ctx, s.Cache, cache.InboundByTagKey(tag), inboundCacheTTL, func(ctx context.Context) (generated.Inbound, error) {
		return s.Queries.GetInboundByTag(ctx, tag)
	})
}

func (s *Store) CachedListInboundTagsByProtocol(ctx context.Context, protocol string) ([]string, error) {
	return cache.GetOrSet(ctx, s.Cache, cache.InboundTagsByProtocolKey(protocol), inboundCacheTTL, func(ctx context.Context) ([]string, error) {
		return s.Queries.ListInboundTagsByProtocol(ctx, protocol)
	})
}

// InvalidateInbound busts both the tag-keyed entry and the protocol-keyed
// tag list. oldProtocol is non-nil only when a re-sync changed an existing
// tag's protocol (the rare case where the OLD protocol's tag list also
// needs busting, since that list no longer includes this tag).
func (s *Store) InvalidateInbound(ctx context.Context, tag, protocol string, oldProtocol *string) error {
	keys := []string{cache.InboundByTagKey(tag), cache.InboundTagsByProtocolKey(protocol)}
	if oldProtocol != nil && *oldProtocol != protocol {
		keys = append(keys, cache.InboundTagsByProtocolKey(*oldProtocol))
	}
	return s.Cache.Del(ctx, keys...)
}

func (s *Store) CachedListHostsByInboundTag(ctx context.Context, tag string) ([]generated.Host, error) {
	return cache.GetOrSet(ctx, s.Cache, cache.InboundHostsKey(tag), inboundCacheTTL, func(ctx context.Context) ([]generated.Host, error) {
		return s.Queries.ListHostsByInboundTag(ctx, tag)
	})
}

func (s *Store) InvalidateHosts(ctx context.Context, tag string) error {
	return s.Cache.Del(ctx, cache.InboundHostsKey(tag))
}

func (s *Store) CachedListExcludedInboundTags(ctx context.Context, proxyID int32) ([]string, error) {
	return cache.GetOrSet(ctx, s.Cache, cache.ExcludedInboundTagsKey(proxyID), excludedInboundsCacheTTL, func(ctx context.Context) ([]string, error) {
		return s.Queries.ListExcludedInboundTags(ctx, proxyID)
	})
}

func (s *Store) InvalidateExcludedInbounds(ctx context.Context, proxyID int32) error {
	return s.Cache.Del(ctx, cache.ExcludedInboundTagsKey(proxyID))
}

// CachedGetIntegrationSettings and InvalidateIntegrationSettings cache the
// single integration_settings row (see internal/integrationsettings for the
// env-fallback merge applied on top of it) under the shared "settings" key
// namespace declared in internal/cache/cache.go, previously unused.
func (s *Store) CachedGetIntegrationSettings(ctx context.Context) (generated.IntegrationSetting, error) {
	return cache.GetOrSet(ctx, s.Cache, cache.SettingsKey(), integrationSettingsCacheTTL, func(ctx context.Context) (generated.IntegrationSetting, error) {
		return s.Queries.GetIntegrationSettings(ctx)
	})
}

func (s *Store) InvalidateIntegrationSettings(ctx context.Context) error {
	return s.Cache.Del(ctx, cache.SettingsKey())
}
