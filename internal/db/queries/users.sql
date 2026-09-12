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

-- name: CreateGatewaySyncedUser :one
-- Gateway sub-phase 2: a replica of a user that genuinely lives on a peer
-- panel, pushed here via POST /api/internal/gateway/users/sync - same
-- shape as CreateUser but with synced_from_panel_name set and no admin_id
-- (a replica has no local owning admin; policy/ownership belongs to the
-- panel that actually owns the user).
INSERT INTO users (
    username, status, data_limit, data_limit_reset_strategy, expire, synced_from_panel_name
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: UpdateGatewaySyncedUser :one
-- Refreshes a replica's policy fields from a later sync push - never
-- touches note/on_hold_*/auto_delete_in_days (never synced in the first
-- place, see gatewaySyncPayload's own doc comment) or synced_from_panel_name
-- itself (a replica never changes which panel it's a replica of).
UPDATE users SET
    status = $2,
    data_limit = $3,
    data_limit_reset_strategy = $4,
    expire = $5,
    edit_at = now()
WHERE id = $1
RETURNING *;

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
--
-- `sort` mirrors the 5 options the old dashboard's own sort dropdown always
-- sent (confirmed by reading the actual served UsersPage bundle) - the Go
-- handler validates it against this exact allow-list before it ever reaches
-- SQL, so this CASE-per-option shape (not string-built ORDER BY) is a
-- defense in depth against a stray value becoming a real injection vector,
-- not the only guard. Every CASE but the one matching `sort` evaluates to
-- NULL and Postgres just skips it, so exactly one of the 5 keys actually
-- orders the result; `id DESC` as the last, always-active key is both the
-- default (the handler defaults `sort` itself to "-created_at", but this
-- covers any caller that skips that entirely) and the tiebreaker for the
-- other 4. Plain `ORDER BY id` (oldest first, no options at all) used to
-- make an admin's newest signups the LAST page instead of the first, the
-- opposite of the old panel's default - found by comparing the two
-- dashboards directly, not assumed.
SELECT * FROM users
WHERE (sqlc.narg('admin_id')::int IS NULL OR admin_id = sqlc.narg('admin_id')::int)
  AND (sqlc.narg('statuses')::text[] IS NULL OR status = ANY(sqlc.narg('statuses')::text[]))
  AND (
    sqlc.narg('search')::text IS NULL
    OR username ILIKE '%' || sqlc.narg('search')::text || '%'
    OR note ILIKE '%' || sqlc.narg('search')::text || '%'
  )
ORDER BY
  CASE WHEN sqlc.narg('sort')::text = 'created_at' THEN created_at END ASC,
  CASE WHEN sqlc.narg('sort')::text = '-created_at' THEN created_at END DESC,
  CASE WHEN sqlc.narg('sort')::text = 'username' THEN username END ASC,
  CASE WHEN sqlc.narg('sort')::text = '-used_traffic' THEN used_traffic END DESC,
  CASE WHEN sqlc.narg('sort')::text = 'expire' THEN expire END ASC,
  id DESC
LIMIT sqlc.narg('limit')::int OFFSET sqlc.narg('offset')::int;

-- name: CountUsers :one
-- Mirrors ListUsers' own WHERE clause exactly (admin_id/statuses/search) so
-- a caller passing the same filters always gets a true total independent of
-- whatever limit/offset it also passed - see handleListUsers' own comment on
-- why this exists (it used to report len(page) as "total", silently wrong
-- for any paginated caller).
SELECT count(*) FROM users
WHERE (sqlc.narg('admin_id')::int IS NULL OR admin_id = sqlc.narg('admin_id')::int)
  AND (sqlc.narg('statuses')::text[] IS NULL OR status = ANY(sqlc.narg('statuses')::text[]))
  AND (
    sqlc.narg('search')::text IS NULL
    OR username ILIKE '%' || sqlc.narg('search')::text || '%'
    OR note ILIKE '%' || sqlc.narg('search')::text || '%'
  );
