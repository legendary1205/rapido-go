-- name: GetPendingResellerAPIUsage :many
-- Everything the reseller bot has not yet accepted, per admin, with the
-- username the bot knows the reseller by.
SELECT q.admin_id, a.username, q.pending
FROM reseller_api_usage_queue q
JOIN admins a ON a.id = q.admin_id
WHERE q.pending > 0
ORDER BY q.admin_id;

-- name: MarkResellerAPIUsageReported :exec
-- Subtracts exactly what was sent rather than zeroing the row: a node
-- report that lands while the POST is in flight adds to `pending`, and that
-- traffic must still go out with the next report.
UPDATE reseller_api_usage_queue q
SET pending = q.pending - v.sent, reported = q.reported + v.sent
FROM (SELECT unnest(sqlc.arg('admin_ids')::int[]) AS admin_id, unnest(sqlc.arg('sent')::bigint[]) AS sent) AS v
WHERE q.admin_id = v.admin_id;
