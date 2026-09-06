-- name: ListHosts :many
-- All hosts for every inbound tag, in one query - GET /api/hosts groups them
-- by inbound_tag in Go rather than issuing one query per tag.
SELECT * FROM hosts ORDER BY inbound_tag, id;

-- name: DeleteHostsByInboundTag :exec
DELETE FROM hosts WHERE inbound_tag = $1;

-- name: CreateHost :one
INSERT INTO hosts (
    remark, address, port, path, sni, host, security, alpn, fingerprint,
    inbound_tag, allowinsecure, is_disabled, mux_enable, fragment_setting,
    noise_setting, random_user_agent, use_sni_as_host
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
)
RETURNING *;
