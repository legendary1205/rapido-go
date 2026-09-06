-- +goose Up
-- Bearer token a node presents when it pushes its own usage/health report
-- to the panel (see internal/httpapi/nodereport.go) - the Go rewrite has
-- no panel-to-node dispatch layer to pull stats over (see the Phase 7.3
-- plan's context), so nodes push instead, authenticated by this secret
-- rather than a client TLS handshake (the mTLS cert issued at node
-- creation authenticates the *panel* to the *node*'s own control-plane
-- port, the opposite direction from a report push).
ALTER TABLE nodes ADD COLUMN report_secret TEXT;
CREATE UNIQUE INDEX nodes_report_secret_key ON nodes (report_secret);

-- The `system` table (fleet-wide cumulative uplink/downlink) was created by
-- migration 00001 but never seeded - nothing needed a row in it until this
-- phase's node-report ingestion started incrementing it. Singleton row,
-- same pattern as 00005's integration_settings seed.
INSERT INTO system (uplink, downlink) VALUES (0, 0);

-- +goose Down
DELETE FROM system;
DROP INDEX nodes_report_secret_key;
ALTER TABLE nodes DROP COLUMN report_secret;
