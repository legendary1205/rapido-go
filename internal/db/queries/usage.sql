-- Queries behind the per-node usage-report endpoints the real panel exposes
-- (GET /api/user/{username}/usage, /api/users/usage, /sub/{token}/usage) and
-- the bulk user-maintenance ones (/api/users/reset, /api/users/expired).
-- Every one of these sums in the database rather than hydrating rows to add
-- them up in Go - see crud.get_all_users_usages's own comment on the real
-- panel for the measured reason (47s -> 5s at ~1.4M usage rows).

-- name: SumUserUsageByNode :many
SELECT node_id, SUM(used_traffic)::bigint AS total
FROM node_user_usages
WHERE user_id = $1 AND created_at >= $2 AND created_at <= $3
GROUP BY node_id;

-- name: SumAllUsersUsageByNode :many
-- The join to users is what keeps out usage rows whose user has since been
-- deleted, matching the real panel's own query shape.
SELECT nu.node_id, SUM(nu.used_traffic)::bigint AS total
FROM node_user_usages nu
JOIN users u ON u.id = nu.user_id
WHERE nu.created_at >= $1 AND nu.created_at <= $2
GROUP BY nu.node_id;

-- name: SumAllUsersUsageByNodeForAdmin :many
SELECT nu.node_id, SUM(nu.used_traffic)::bigint AS total
FROM node_user_usages nu
JOIN users u ON u.id = nu.user_id
WHERE nu.created_at >= $1 AND nu.created_at <= $2 AND u.admin_id = $3
GROUP BY nu.node_id;

-- name: ListExpiredUsers :many
-- 'limited' counts as expired here too, exactly as the real panel's
-- get_expired_users_list does - both mean "this user can no longer connect
-- and is a candidate for cleanup".
SELECT id, username, admin_id FROM users
WHERE status IN ('expired', 'limited')
  AND expire IS NOT NULL AND expire >= $1 AND expire <= $2
ORDER BY id;

-- name: ListExpiredUsersByAdmin :many
SELECT id, username, admin_id FROM users
WHERE status IN ('expired', 'limited')
  AND expire IS NOT NULL AND expire >= $1 AND expire <= $2
  AND admin_id = $3
ORDER BY id;

-- name: DeleteUsersByIDs :exec
DELETE FROM users WHERE id = ANY(sqlc.arg('ids')::int[]);

-- name: ListUserIDsByAdmin :many
SELECT id FROM users WHERE admin_id = $1;

-- name: ListAllUserIDs :many
SELECT id FROM users;

-- name: ResetUsersUsageByIDs :exec
-- Mirrors reset_all_users_data_usage: zero the counter, and reactivate only
-- users that aren't deliberately parked (on_hold) or already finished
-- (expired/disabled) - resetting traffic must never silently un-disable an
-- account an admin switched off on purpose.
UPDATE users SET used_traffic = 0,
    status = CASE WHEN status IN ('on_hold', 'expired', 'disabled') THEN status ELSE 'active' END
WHERE id = ANY(sqlc.arg('ids')::int[]);

-- name: DeleteNodeUserUsagesByUserIDs :exec
DELETE FROM node_user_usages WHERE user_id = ANY(sqlc.arg('ids')::int[]);

-- name: DeleteUserUsageLogsByUserIDs :exec
DELETE FROM user_usage_logs WHERE user_id = ANY(sqlc.arg('ids')::int[]);

-- name: DeleteNextPlansByUserIDs :exec
DELETE FROM next_plans WHERE user_id = ANY(sqlc.arg('ids')::int[]);

-- name: SetUserOwner :one
UPDATE users SET admin_id = $2 WHERE id = $1 RETURNING *;
