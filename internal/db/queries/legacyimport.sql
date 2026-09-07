-- name: ImportAdmin :one
-- Like CreateAdmin, but also sets the historical fields a fresh admin
-- signup would never have (created_at, password_reset_at, users_usage) -
-- a legacy-panel import needs to preserve these exactly, not reset them to
-- "just now" the way a brand-new admin creation naturally would.
INSERT INTO admins (username, hashed_password, created_at, is_sudo, password_reset_at, telegram_id, discord_webhook, users_usage)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ImportUser :one
-- Like CreateUser, but also sets every historical/runtime field a fresh
-- user creation never has (used_traffic, created_at, sub_revoked_at,
-- sub_updated_at, sub_last_user_agent, online_at, edit_at,
-- last_status_change) - same rationale as ImportAdmin.
INSERT INTO users (
    username, status, used_traffic, data_limit, expire, created_at, admin_id,
    data_limit_reset_strategy, sub_revoked_at, note, sub_updated_at,
    sub_last_user_agent, online_at, edit_at, on_hold_timeout,
    on_hold_expire_duration, auto_delete_in_days, last_status_change
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
RETURNING *;

-- name: TruncateForLegacyImport :exec
-- The "full replacement" step of a legacy-panel import: wipes every table
-- an import actually populates, RESTART IDENTITY so new rows get a clean
-- 1-based id sequence instead of continuing from wherever the panel's own
-- prior data left off. Deliberately does NOT touch tls/jwt_secrets/
-- integration_settings/core_config/tickets/ticket_messages/host_metrics/
-- node_usages/node_user_usages - importing another panel's user data
-- should never overwrite THIS panel's own identity material, and the
-- excluded historical/usage tables aren't part of what any importer
-- populates (see the Phase 8.2 plan's explicit scope notes).
TRUNCATE admins, users, proxies, hosts, inbounds, exclude_inbounds_association,
    template_inbounds_association, user_templates, next_plans
    RESTART IDENTITY CASCADE;
