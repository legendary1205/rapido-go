-- +goose Up
-- Same interim-sync-metadata gap as 00002/00003: an inbound's own TLS mode
-- (none/tls/reality) and REALITY parameters (private key, short ids,
-- handshake target) live only in the current system's parsed core config,
-- needed by subscription generation to build correct links even for a host
-- whose own `security` column is "inbound_default".
ALTER TABLE inbounds ADD COLUMN security TEXT NOT NULL DEFAULT 'none'
    CHECK (security IN ('none', 'tls', 'reality'));
ALTER TABLE inbounds ADD COLUMN reality_private_key TEXT;
ALTER TABLE inbounds ADD COLUMN reality_short_ids TEXT[];
ALTER TABLE inbounds ADD COLUMN reality_server_name TEXT;
ALTER TABLE inbounds ADD COLUMN reality_server_port INTEGER;

-- +goose Down
ALTER TABLE inbounds DROP COLUMN reality_server_port;
ALTER TABLE inbounds DROP COLUMN reality_server_name;
ALTER TABLE inbounds DROP COLUMN reality_short_ids;
ALTER TABLE inbounds DROP COLUMN reality_private_key;
ALTER TABLE inbounds DROP COLUMN security;
