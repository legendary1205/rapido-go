-- name: GetIntegrationSettings :one
-- Singleton row (seeded once by migration 00005) - NULL in any column means
-- "use the env value" (see internal/integrationsettings.Resolve).
SELECT * FROM integration_settings ORDER BY id LIMIT 1;

-- name: UpdateIntegrationSettings :one
-- Every column is set from the caller's already-merged values (current row
-- + only the PATCH request's present fields - see
-- handleUpdateIntegrationSettings, which must fetch-then-merge itself since
-- a column here can be explicitly cleared back to NULL, unlike UpdateAdmin's
-- simpler "truthy overwrites" rule).
UPDATE integration_settings SET
    kirbot_secret = $1,
    kirbot_url = $2,
    kirbot_license = $3,
    telegram_api_token = $4,
    telegram_admin_ids = $5,
    telegram_proxy_url = $6,
    telegram_logger_channel_id = $7,
    telegram_logger_topic_id = $8,
    telegram_default_vless_flow = $9,
    webhook_addresses = $10,
    webhook_secret = $11,
    discord_webhook_url = $12,
    updated_at = now()
WHERE id = (SELECT id FROM integration_settings ORDER BY id LIMIT 1)
RETURNING *;
