-- name: GetUsersByUsernames :many
-- Resolves a node report's usernames to (id, admin_id) in one round trip -
-- a push tick only carries usernames (all the node ever knows about a
-- user), the panel needs the id/admin_id to write used_traffic and the
-- per-admin aggregate.
SELECT id, username, admin_id FROM users WHERE username = ANY(sqlc.arg('usernames')::text[]);

-- name: IncrementUserUsage :exec
-- online_at is set in the same statement as used_traffic, deliberately:
-- it should mean "this user actually moved traffic just now", the same
-- instant the traffic itself is recorded, not a separate connect/
-- disconnect event this rewrite has no way to observe anyway (mirrors
-- record_usages.py's own combined UPDATE).
UPDATE users SET used_traffic = used_traffic + $2, online_at = now() WHERE id = $1;

-- name: IncrementAdminUsage :exec
UPDATE admins SET users_usage = users_usage + $2 WHERE id = $1;

-- name: UpsertNodeUserUsage :exec
-- created_at is the caller-computed current-hour bucket (truncated to the
-- hour before being passed in) - the UNIQUE(created_at,user_id,node_id)
-- constraint from migration 00001 is what makes this a real upsert instead
-- of a duplicate row per push tick within the same hour.
INSERT INTO node_user_usages (created_at, user_id, node_id, used_traffic)
VALUES ($1, $2, $3, $4)
ON CONFLICT (created_at, user_id, node_id)
DO UPDATE SET used_traffic = node_user_usages.used_traffic + EXCLUDED.used_traffic;

-- name: UpsertNodeUsage :exec
INSERT INTO node_usages (created_at, node_id, uplink, downlink)
VALUES ($1, $2, $3, $4)
ON CONFLICT (created_at, node_id)
DO UPDATE SET uplink = node_usages.uplink + EXCLUDED.uplink,
              downlink = node_usages.downlink + EXCLUDED.downlink;

-- name: GetDailyUsageHistory :many
-- Feeds GET /api/system/usage-history - zero-filling missing days is the
-- caller's job (this only returns days that actually have a node_usages
-- row), matching crud.get_daily_usage_history's own shape.
SELECT date_trunc('day', created_at)::date AS day,
       COALESCE(SUM(uplink + downlink), 0)::bigint AS usage
FROM node_usages
WHERE created_at >= $1
GROUP BY day
ORDER BY day;

-- name: GetSystem :one
SELECT * FROM system ORDER BY id LIMIT 1;

-- name: IncrementSystemCumulative :exec
-- system is a genuine singleton (seeded once by migration 00006, never a
-- second row inserted anywhere) - no WHERE needed, this updates its one row.
UPDATE system SET uplink = uplink + $1, downlink = downlink + $2;

-- name: InsertHostMetric :one
INSERT INTO host_metrics (node_id, collected_at, cpu_percent, mem_percent, disk_percent,
                          rx_rate, tx_rate, connections, tunnels_up, tunnels_total, healthy, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetLatestHostMetricPerNode :many
-- One row per host (every node plus the panel's own node_id-NULL row) -
-- GROUP BY node_id / MAX(id) then a self-join back to the full row, not an
-- N+1 loop. NULLS NOT DISTINCT-style grouping works naturally here since
-- Postgres GROUP BY already treats every NULL as one group.
SELECT hm.* FROM host_metrics hm
JOIN (
    SELECT node_id, MAX(id) AS max_id FROM host_metrics GROUP BY node_id
) latest ON latest.max_id = hm.id;

-- name: GetHostMetricHistory :many
SELECT * FROM host_metrics
WHERE (sqlc.narg('node_id')::int IS NULL AND node_id IS NULL OR node_id = sqlc.narg('node_id')::int)
  AND collected_at >= $1
ORDER BY collected_at;

-- name: PruneOldHostMetrics :execrows
DELETE FROM host_metrics WHERE collected_at < $1;
