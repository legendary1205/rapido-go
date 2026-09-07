-- name: GetGatewaySecret :one
SELECT * FROM gateway_settings ORDER BY id LIMIT 1;

-- name: CreateGatewaySecret :one
INSERT INTO gateway_settings (secret) VALUES ($1)
RETURNING *;

-- name: RotateGatewaySecret :one
UPDATE gateway_settings SET secret = $1
WHERE id = (SELECT id FROM gateway_settings ORDER BY id LIMIT 1)
RETURNING *;

-- name: SetGatewayName :one
UPDATE gateway_settings SET name = $1
WHERE id = (SELECT id FROM gateway_settings ORDER BY id LIMIT 1)
RETURNING *;

-- name: ListGatewayPeers :many
SELECT * FROM gateway_peers ORDER BY id;

-- name: GetGatewayPeer :one
SELECT * FROM gateway_peers WHERE id = $1;

-- name: CreateGatewayPeer :one
INSERT INTO gateway_peers (name, base_url, secret, enabled)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateGatewayPeer :one
UPDATE gateway_peers SET name = $2, base_url = $3, secret = $4, enabled = $5
WHERE id = $1
RETURNING *;

-- name: DeleteGatewayPeer :exec
DELETE FROM gateway_peers WHERE id = $1;
