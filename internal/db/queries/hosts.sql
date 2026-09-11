-- name: ListHosts :many
-- All hosts for every inbound tag, in one query - GET /api/hosts groups them
-- by inbound_tag in Go rather than issuing one query per tag. Ordered by the
-- admin-controlled global priority (see migration 00008), not inbound_tag -
-- a host's own inbound_tag column still tells the caller which tag it
-- belongs to, this ordering is purely display/subscription sequence.
SELECT * FROM hosts ORDER BY priority, id;

-- name: ListHostsByInboundTags :many
-- Excludes disabled hosts - matches the current system's global exclusion
-- in app/xray/__init__.py's hosts DictStorage builder (a disabled host
-- never appears in subscription output for any format, not a per-format
-- decision). Ordered by priority (see migration 00008), globally across
-- every tag in the array - forEachUserHost (internal/httpapi/
-- subscription.go) needs every included tag's hosts interleaved by this
-- one cross-tag order, not grouped per tag, so the admin's global priority
-- actually reaches subscription output. Takes the whole tag set in one
-- call rather than one round trip per tag - see that function's own doc
-- comment on the N+1 this replaced.
SELECT * FROM hosts WHERE inbound_tag = ANY(sqlc.arg('tags')::text[]) AND (is_disabled IS NULL OR is_disabled = false) ORDER BY priority, id;

-- name: DeleteHostsByInboundTag :exec
DELETE FROM hosts WHERE inbound_tag = $1;

-- name: GetMaxHostPriority :one
-- Used to place a freshly auto-created default host (see
-- internal/httpapi/inbounds.go's createDefaultHost) at the END of the
-- existing global order, matching what the Hosts page's own "+ Add host"
-- button already does client-side (rapido-ui/hostsReducers.ts's
-- maxPriority) - a host inserted with the bare column default (0) instead
-- would collide with every other host already at priority 0 and make the
-- up/down reorder buttons look broken (swapping two equal values is a
-- real no-op, not a bug in the swap itself).
SELECT COALESCE(MAX(priority), -1)::int AS max_priority FROM hosts;

-- name: CreateHost :one
INSERT INTO hosts (
    remark, address, port, path, sni, host, security, alpn, fingerprint,
    inbound_tag, allowinsecure, is_disabled, mux_enable, fragment_setting,
    noise_setting, random_user_agent, use_sni_as_host, priority
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
)
RETURNING *;
