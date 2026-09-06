-- name: CreateUser :one
INSERT INTO users (
    username, status, data_limit, data_limit_reset_strategy, expire,
    admin_id, note, on_hold_expire_duration, on_hold_timeout, auto_delete_in_days
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
RETURNING *;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: DeleteUser :exec
-- Cascades to proxies (and from there to exclude_inbounds_association),
-- next_plans, user_usage_logs, node_user_usages, notification_reminders and
-- tickets - see the ON DELETE CASCADE fixes in 00001_init_schema.sql.
DELETE FROM users WHERE id = $1;

-- name: UpdateUserCore :one
-- Replaces every core field at once with values the caller has already
-- computed (including the status-transition side effects that mirror
-- crud.update_user - see internal/httpapi/user.go). edit_at is always
-- bumped to now(), matching the unconditional `dbuser.edit_at =
-- datetime.utcnow()` at the end of the current update_user.
UPDATE users SET
    status = $2,
    data_limit = $3,
    data_limit_reset_strategy = $4,
    expire = $5,
    note = $6,
    on_hold_expire_duration = $7,
    on_hold_timeout = $8,
    auto_delete_in_days = $9,
    edit_at = now()
WHERE id = $1
RETURNING *;

-- name: ResetUserTraffic :one
-- used by both the manual reset-usage endpoint and NextPlan firing (Phase
-- 5) - status is passed in already computed (crud.reset_user_data_usage
-- reactivates unless the user is expired/disabled).
UPDATE users SET used_traffic = 0, status = $2 WHERE id = $1
RETURNING *;

-- name: UpdateUserSub :exec
-- Mirrors crud.update_user_sub: recorded on every hit of the auto-detect
-- subscription route (not the explicit-format or info/usage routes).
UPDATE users SET sub_updated_at = now(), sub_last_user_agent = $2 WHERE id = $1;

-- name: SetUserSubRevoked :one
UPDATE users SET sub_revoked_at = now() WHERE id = $1
RETURNING *;

-- name: ListUsers :many
-- Every filter is optional (NULL disables it) so one query serves both the
-- sudo "all users" and the non-sudo "only my users" cases, matching
-- crud.get_users. limit/offset NULL means unbounded/0, same as ListAdmins.
SELECT * FROM users
WHERE (sqlc.narg('admin_id')::int IS NULL OR admin_id = sqlc.narg('admin_id')::int)
  AND (sqlc.narg('statuses')::text[] IS NULL OR status = ANY(sqlc.narg('statuses')::text[]))
  AND (
    sqlc.narg('search')::text IS NULL
    OR username ILIKE '%' || sqlc.narg('search')::text || '%'
    OR note ILIKE '%' || sqlc.narg('search')::text || '%'
  )
ORDER BY id
LIMIT sqlc.narg('limit')::int OFFSET sqlc.narg('offset')::int;

-- name: CountUsers :one
SELECT count(*) FROM users
WHERE (sqlc.narg('admin_id')::int IS NULL OR admin_id = sqlc.narg('admin_id')::int)
  AND (sqlc.narg('statuses')::text[] IS NULL OR status = ANY(sqlc.narg('statuses')::text[]));
