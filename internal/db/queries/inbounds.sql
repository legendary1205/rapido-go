-- name: UpsertInbound :one
-- Registers a known (tag, protocol) pair - a stand-in for syncing from the
-- live proxy core config until the node-agent phase does that for real.
-- `inserted` tells the caller whether this created a brand new inbound (in
-- which case it should also create that inbound's default host, mirroring
-- add_default_host in the current crud.get_or_create_inbound).
INSERT INTO inbounds (
    tag, protocol, network, header_type, security,
    reality_private_key, reality_short_ids, reality_server_name, reality_server_port
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tag) DO UPDATE SET
    protocol = EXCLUDED.protocol,
    network = EXCLUDED.network,
    header_type = EXCLUDED.header_type,
    security = EXCLUDED.security,
    reality_private_key = EXCLUDED.reality_private_key,
    reality_short_ids = EXCLUDED.reality_short_ids,
    reality_server_name = EXCLUDED.reality_server_name,
    reality_server_port = EXCLUDED.reality_server_port
RETURNING *, (xmax = 0) AS inserted;

-- name: GetInboundByTag :one
SELECT * FROM inbounds WHERE tag = $1;

-- name: ListInbounds :many
SELECT * FROM inbounds ORDER BY protocol, tag;

-- name: ListInboundTagsByProtocol :many
SELECT tag FROM inbounds WHERE protocol = $1 ORDER BY tag;

-- name: DeleteInboundByTag :exec
-- Cascades to that inbound's hosts, exclude_inbounds_association rows, and
-- template_inbounds_association rows (all ON DELETE CASCADE - see
-- 00001_init_schema.sql) - no separate cleanup query needed.
DELETE FROM inbounds WHERE tag = $1;
