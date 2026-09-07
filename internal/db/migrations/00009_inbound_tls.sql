-- +goose Up
-- Real customer-facing TLS for an inbound (as opposed to the panel's own
-- CA used for node mTLS, in the `tls` table) - plain text, same
-- no-encryption-at-rest convention `reality_private_key` already uses.
-- Nullable/empty is the normal state until an admin actually pastes a
-- certificate; ListAutoSyncInbounds only includes a security='tls' inbound
-- once both are non-empty, so this is purely additive - no existing
-- inbound's behavior changes.
ALTER TABLE inbounds ADD COLUMN tls_certificate TEXT;
ALTER TABLE inbounds ADD COLUMN tls_key TEXT;
ALTER TABLE inbounds ADD COLUMN tls_server_name TEXT;

-- +goose Down
ALTER TABLE inbounds DROP COLUMN tls_certificate;
ALTER TABLE inbounds DROP COLUMN tls_key;
ALTER TABLE inbounds DROP COLUMN tls_server_name;
