-- name: CreateAdmin :one
INSERT INTO admins (username, hashed_password, is_sudo, telegram_id, discord_webhook)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetAdminByUsername :one
SELECT * FROM admins WHERE username = $1;

-- name: GetAdminByID :one
SELECT * FROM admins WHERE id = $1;

-- name: ListAdmins :many
-- limit/offset NULL means "unbounded" / "0" respectively - Postgres treats
-- LIMIT NULL and OFFSET NULL that way natively, matching crud.get_admins'
-- "only apply if truthy" behavior without needing COALESCE.
SELECT * FROM admins
WHERE (sqlc.narg('username')::text IS NULL OR username ILIKE '%' || sqlc.narg('username')::text || '%')
ORDER BY id
LIMIT sqlc.narg('limit')::int OFFSET sqlc.narg('offset')::int;

-- name: UpdateAdmin :one
UPDATE admins
SET
    is_sudo = $2,
    hashed_password = $3,
    password_reset_at = $4,
    telegram_id = $5,
    discord_webhook = $6
WHERE id = $1
RETURNING *;

-- name: DeleteAdmin :exec
DELETE FROM admins WHERE id = $1;

-- name: ResetAdminUsage :one
-- Zeroes an admin's running usage counter, archiving the prior value into
-- admin_usage_logs first - mirrors crud.reset_admin_usage's log-then-zero
-- sequence so historical usage isn't lost.
WITH logged AS (
    INSERT INTO admin_usage_logs (admin_id, used_traffic_at_reset)
    SELECT admins.id, users_usage FROM admins WHERE admins.id = $1 AND users_usage != 0
)
UPDATE admins SET users_usage = 0 WHERE admins.id = $1
RETURNING *;

-- name: GetInactiveAdmins :many
-- Non-sudo admins whose most recent real activity - a user they created,
-- edited or renewed - predates the cutoff. An admin with no users at all is
-- judged by when the admin itself was created. Sudo admins are account
-- owners and are never eligible.
SELECT
    a.*,
    COALESCE(MAX(COALESCE(u.edit_at, u.created_at)), a.created_at)::timestamptz AS last_activity,
    COUNT(u.id)::bigint AS user_count
FROM admins a
LEFT JOIN users u ON u.admin_id = a.id
WHERE a.is_sudo = false
GROUP BY a.id
HAVING COALESCE(MAX(COALESCE(u.edit_at, u.created_at)), a.created_at) < $1
ORDER BY last_activity ASC;

-- name: DeleteUsersByAdminID :many
-- DB-side half of the inactive-admin cleanup flow. Node-side teardown
-- (removing each user from the live core config) is dispatched by the
-- caller from the returned rows - it depends on the node/RPC layer built in
-- a later phase and isn't performed here.
DELETE FROM users WHERE admin_id = $1
RETURNING *;

-- name: DisableActiveUsersByAdminID :many
-- Mirrors crud.disable_all_active_users(admin=...): every active or
-- on_hold user under this admin goes to disabled in one statement - no
-- separate node-side dispatch needed, since node config is polled with a
-- short cache TTL (see nodeConfigCacheTTL) rather than push-invalidated.
UPDATE users SET status = 'disabled', last_status_change = now()
WHERE admin_id = $1 AND status IN ('active', 'on_hold')
RETURNING *;

-- name: ActivateDisabledUsersByAdminID :many
-- Mirrors crud.activate_all_disabled_users(admin=..., users_limit=None)'s
-- actual effect for that call shape: every disabled user under this admin
-- goes back to active unconditionally. (The Python version also has an
-- on_hold-reclassification pass, but it runs against the same rows this
-- statement just flipped to active within the same transaction, so it
-- never matches anything for this call shape - not replicated here since
-- it would be dead code either way.)
UPDATE users SET status = 'active', last_status_change = now()
WHERE admin_id = $1 AND status = 'disabled'
RETURNING *;
