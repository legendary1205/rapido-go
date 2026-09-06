-- name: UpsertInbound :one
-- Registers a known (tag, protocol) pair - a stand-in for syncing from the
-- live proxy core config until the node-agent phase does that for real.
-- `inserted` tells the caller whether this created a brand new inbound (in
-- which case it should also create that inbound's default host, mirroring
-- add_default_host in the current crud.get_or_create_inbound).
INSERT INTO inbounds (tag, protocol) VALUES ($1, $2)
ON CONFLICT (tag) DO UPDATE SET protocol = EXCLUDED.protocol
RETURNING *, (xmax = 0) AS inserted;

-- name: GetInboundByTag :one
SELECT * FROM inbounds WHERE tag = $1;

-- name: ListInbounds :many
SELECT * FROM inbounds ORDER BY protocol, tag;

-- name: ListInboundTagsByProtocol :many
SELECT tag FROM inbounds WHERE protocol = $1 ORDER BY tag;
