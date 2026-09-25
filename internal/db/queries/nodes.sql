-- name: CreateNode :one
INSERT INTO nodes (name, address, port, api_port, usage_coefficient, report_secret, inbound_tags, listen_ports, core_overrides)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetNodeByID :one
SELECT * FROM nodes WHERE id = $1;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY id;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = $1;

-- name: UpdateNode :one
-- Full-field update, matching the current Python system's PUT /node/{id}
-- semantics - a client always sends the whole object back, not a partial
-- patch. usage_coefficient stays out of scope here deliberately when the
-- caller doesn't know it changed: sqlc.narg lets a NULL mean "keep the
-- stored value" so a bare create-shaped payload doesn't reset it to 1.0.
UPDATE nodes SET
    name = $2,
    address = $3,
    port = $4,
    api_port = $5,
    usage_coefficient = COALESCE(sqlc.narg('usage_coefficient')::float8, usage_coefficient),
    status = CASE WHEN sqlc.narg('disabled')::bool IS TRUE THEN 'disabled'
                  WHEN sqlc.narg('disabled')::bool IS FALSE AND status = 'disabled' THEN 'connecting'
                  ELSE status END,
    inbound_tags = sqlc.narg('inbound_tags')::text[],
    listen_ports = sqlc.narg('listen_ports')::int[],
    core_overrides = sqlc.arg('core_overrides')::jsonb
WHERE id = $1
RETURNING *;

-- name: GetNodeByReportSecret :one
-- Identifies which node is pushing a report - see
-- internal/httpapi/nodereport.go. A node authenticates by bearer secret,
-- not by claiming its own id in the payload.
SELECT * FROM nodes WHERE report_secret = $1;

-- name: MarkNodeConnectedIfNotDisabled :exec
-- The push model has no separate health-check poller (see the Phase 7.3
-- plan's context) - receiving any report at all is itself the liveness
-- signal, so the first one flips a node from its initial 'connecting' (or
-- a stale 'error') to 'connected'. Never overrides an admin's explicit
-- 'disabled'.
UPDATE nodes SET status = 'connected', last_status_change = now()
WHERE id = $1 AND status != 'disabled' AND status != 'connected';

-- name: IncrementNodeCumulative :exec
UPDATE nodes SET uplink = uplink + $2, downlink = downlink + $3 WHERE id = $1;

-- name: GetNodesUsage :many
-- Sums node_usages in [start,end] per node, seeding a zero row for every
-- node that moved no traffic in the window (a plain SUM...GROUP BY would
-- silently omit it) - mirrors crud.get_nodes_usage's zero-seed-then-sum
-- shape. The "Master" (local core, node_id NULL) row Python also seeds
-- doesn't apply here: the Go rewrite's node agent is always a separate
-- process, there is no local-core traffic to report.
SELECT n.id AS node_id, n.name AS node_name,
       COALESCE(SUM(u.uplink), 0)::bigint AS uplink,
       COALESCE(SUM(u.downlink), 0)::bigint AS downlink
FROM nodes n
-- Inclusive upper bound, matching the real panel: a row stamped exactly at
-- `end` belongs to the window, and node_usages rows land on a fixed tick,
-- so an exclusive bound silently drops a whole bucket for a client paging
-- a month by back-to-back hour boundaries.
LEFT JOIN node_usages u ON u.node_id = n.id AND u.created_at >= $1 AND u.created_at <= $2
GROUP BY n.id, n.name
ORDER BY n.id;
