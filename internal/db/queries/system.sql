-- name: CountUsersByStatus :many
-- Feeds GET /api/system's per-status breakdown - admin_id NULL means
-- unscoped (sudo sees the whole fleet).
SELECT status, count(*) AS count FROM users
WHERE (sqlc.narg('admin_id')::int IS NULL OR admin_id = sqlc.narg('admin_id')::int)
GROUP BY status;

-- name: CountOnlineUsersSince :one
-- "Online" means online_at within the caller-computed cutoff (matches the
-- dashboard's own 180s presence window). Always 0 today since nothing yet
-- writes online_at - see internal/httpapi/system.go's comment.
SELECT count(*) FROM users
WHERE online_at >= sqlc.arg('cutoff')::timestamptz
  AND (sqlc.narg('admin_id')::int IS NULL OR admin_id = sqlc.narg('admin_id')::int);

-- name: CountAdminUsersByUsernames :one
-- The scoped (reseller) half of the presence-based online count: Redis knows
-- which usernames are online right now, this counts how many of them belong to
-- one admin. Served by users_username_key, so the cost follows the number of
-- online usernames, not the size of the users table.
SELECT count(*) FROM users
WHERE username = ANY(sqlc.arg('usernames')::text[])
  AND admin_id = sqlc.arg('admin_id')::int;
