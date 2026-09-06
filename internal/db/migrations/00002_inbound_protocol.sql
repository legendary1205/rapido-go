-- +goose Up
-- The current Python system has no protocol column on its "inbounds" table
-- at all - it validates a user's inbound tags against the *live, parsed*
-- xray/sing-box config file's inbounds_by_protocol map, and the DB table is
-- populated lazily and incidentally by whatever tag reference is written
-- first. That's a hard dependency the Go rewrite doesn't have yet (the
-- node/core integration is a later phase), so ProxyInbound needs to know
-- protocol itself in the meantime - upserted by a sync step for now,
-- replaced by real config-derived sync in the node-agent phase.
ALTER TABLE inbounds ADD COLUMN protocol TEXT NOT NULL DEFAULT 'vmess'
    CHECK (protocol IN ('vmess', 'vless', 'trojan', 'shadowsocks'));
ALTER TABLE inbounds ALTER COLUMN protocol DROP DEFAULT;
CREATE INDEX inbounds_protocol_idx ON inbounds (protocol);

-- +goose Down
DROP INDEX IF EXISTS inbounds_protocol_idx;
ALTER TABLE inbounds DROP COLUMN protocol;
