-- +goose Up
-- Singleton row, every column NULL ("use the env value") - seeded here
-- (not lazily on first read) so GetIntegrationSettings can always assume
-- exactly one row exists, with no race between two processes booting for
-- the first time simultaneously. Mirrors the current Python system's own
-- 107fb4d174df_integration_settings.py migration, which seeds the same way.
INSERT INTO integration_settings DEFAULT VALUES;

-- +goose Down
DELETE FROM integration_settings;
