-- name: UpsertInbound :one
-- Registers a known (tag, protocol) pair - a stand-in for syncing from the
-- live proxy core config until the node-agent phase does that for real.
-- `inserted` tells the caller whether this created a brand new inbound (in
-- which case it should also create that inbound's default host, mirroring
-- add_default_host in the current crud.get_or_create_inbound).
INSERT INTO inbounds (
    tag, protocol, network, header_type, security,
    reality_private_key, reality_short_ids, reality_server_name, reality_server_port,
    tls_certificate, tls_key, tls_server_name
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (tag) DO UPDATE SET
    protocol = EXCLUDED.protocol,
    network = EXCLUDED.network,
    header_type = EXCLUDED.header_type,
    security = EXCLUDED.security,
    reality_private_key = EXCLUDED.reality_private_key,
    reality_short_ids = EXCLUDED.reality_short_ids,
    reality_server_name = EXCLUDED.reality_server_name,
    reality_server_port = EXCLUDED.reality_server_port,
    tls_certificate = EXCLUDED.tls_certificate,
    tls_key = EXCLUDED.tls_key,
    tls_server_name = EXCLUDED.tls_server_name
RETURNING *, (xmax = 0) AS inserted;

-- name: GetInboundByTag :one
SELECT * FROM inbounds WHERE tag = $1;

-- name: ListInboundsByTags :many
-- Bulk form of GetInboundByTag - forEachUserHost used to call the single-tag
-- version once per host (every host needs its own inbound's transport/TLS
-- shape), which duplicates work across hosts sharing the same tag and costs
-- one round trip per host. The caller builds a tag->Inbound map from this
-- once per proxy instead.
SELECT * FROM inbounds WHERE tag = ANY(sqlc.arg('tags')::text[]);

-- name: ListInbounds :many
SELECT * FROM inbounds ORDER BY protocol, tag;

-- name: ListInboundsWithPort :many
-- GET /api/inbounds returns one object per inbound carrying the transport
-- shape AND the port a client would actually connect to, which lives on the
-- inbound's primary host (lowest id with a real port), not on the inbound
-- row itself. Same "primary host" rule ListAutoSyncInbounds uses, but
-- without its auto-sync eligibility filter: this endpoint lists every
-- inbound an admin has, configured or not. `ports` is every distinct port of
-- the inbound's non-disabled hosts, ascending (a multi-port inbound listens
-- on all of them); `port` stays the single primary-host value older clients
-- read.
SELECT i.tag, i.protocol, i.network, i.security,
       (SELECT h.port FROM hosts h
        WHERE h.inbound_tag = i.tag AND h.port IS NOT NULL
        ORDER BY h.id LIMIT 1) AS port,
       ARRAY(
           SELECT DISTINCT hp.port FROM hosts hp
           WHERE hp.inbound_tag = i.tag AND hp.is_disabled IS NOT TRUE AND hp.port IS NOT NULL
           ORDER BY hp.port
       )::int[] AS ports
FROM inbounds i
ORDER BY i.protocol, i.tag;

-- name: ListInboundTagsByProtocol :many
SELECT tag FROM inbounds WHERE protocol = $1 ORDER BY tag;

-- name: ListInboundTags :many
-- Every real inbound tag, no other columns - used by PUT
-- /api/settings/core-config to validate a routing rule's `inbound` field
-- references real tags without pulling every column ListInbounds would.
SELECT tag FROM inbounds ORDER BY tag;

-- name: DeleteInboundByTag :exec
-- Cascades to that inbound's hosts, exclude_inbounds_association rows, and
-- template_inbounds_association rows (all ON DELETE CASCADE - see
-- 00001_init_schema.sql) - no separate cleanup query needed.
DELETE FROM inbounds WHERE tag = $1;
