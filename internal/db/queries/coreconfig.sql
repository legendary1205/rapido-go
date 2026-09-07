-- name: GetCoreConfig :one
SELECT * FROM core_config ORDER BY id LIMIT 1;

-- name: UpdateCoreConfig :one
-- core_config is a genuine singleton (seeded once by migration 00007) -
-- no WHERE needed, this updates its one row. Full-replace semantics,
-- matching PUT /hosts's own convention.
UPDATE core_config SET
    log_level = $1,
    sniff_enabled = $2,
    outbounds = $3,
    routing_rules = $4,
    dns_servers = $5,
    updated_at = now()
RETURNING *;
