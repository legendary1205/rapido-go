-- +goose Up

-- Per-node configuration. NULL / '{}' means "the same as every other node",
-- which is exactly what every node received before this migration.
--   inbound_tags    only these inbound tags are served (NULL = every inbound)
--   listen_ports    only these ports of the served inbounds (NULL = every port)
--   core_overrides  optional keys applied over the fleet core config for this
--                   node alone: log_level, sniff_enabled, dns_servers,
--                   outbounds (merged by tag), routing_rules_first
ALTER TABLE nodes
    ADD COLUMN inbound_tags   TEXT[],
    ADD COLUMN listen_ports   INTEGER[],
    ADD COLUMN core_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

-- A change counter per kind of data. The node-config endpoint compares one
-- number instead of rebuilding a ~1 MB payload to find out nothing changed.
CREATE TABLE data_versions (
    name    TEXT PRIMARY KEY,
    version BIGINT NOT NULL DEFAULT 0
);
INSERT INTO data_versions (name) VALUES ('node_config');

-- Bumps by at least one, and never below the wall clock in microseconds. The
-- floor is what keeps a version from ever repeating after the counter is
-- rolled back (a restored backup carries an old value): the next bump jumps
-- past everything handed out before, so a process that cached content under
-- version N can never mistake a different state for it.
-- +goose StatementBegin
CREATE FUNCTION bump_data_version(p_name TEXT) RETURNS BIGINT AS $$
    INSERT INTO data_versions AS d (name, version)
    VALUES (p_name, (extract(epoch FROM clock_timestamp()) * 1000000)::bigint)
    ON CONFLICT (name) DO UPDATE SET version = GREATEST(d.version + 1, EXCLUDED.version)
    RETURNING d.version;
$$ LANGUAGE sql;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION bump_node_config_version() RETURNS trigger AS $$
BEGIN
    PERFORM bump_data_version('node_config');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Statement-level on purpose: a bulk UPDATE/DELETE bumps once, not once per
-- row. Everything that can change what any node is told is listed here; what
-- is deliberately NOT listed is the traffic that changes every few seconds -
-- users.used_traffic / online_at and nodes.status / uplink / downlink /
-- last_status_change. A column-listed UPDATE trigger only fires when the
-- statement's SET list names one of those columns, so those writes cannot
-- reach it.
CREATE TRIGGER node_config_version_hosts
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON hosts
    FOR EACH STATEMENT EXECUTE FUNCTION bump_node_config_version();

CREATE TRIGGER node_config_version_inbounds
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON inbounds
    FOR EACH STATEMENT EXECUTE FUNCTION bump_node_config_version();

CREATE TRIGGER node_config_version_core_config
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON core_config
    FOR EACH STATEMENT EXECUTE FUNCTION bump_node_config_version();

CREATE TRIGGER node_config_version_proxies
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON proxies
    FOR EACH STATEMENT EXECUTE FUNCTION bump_node_config_version();

CREATE TRIGGER node_config_version_users_rows
    AFTER INSERT OR DELETE OR TRUNCATE ON users
    FOR EACH STATEMENT EXECUTE FUNCTION bump_node_config_version();

CREATE TRIGGER node_config_version_users_identity
    AFTER UPDATE OF status, username ON users
    FOR EACH STATEMENT EXECUTE FUNCTION bump_node_config_version();

CREATE TRIGGER node_config_version_nodes_rows
    AFTER INSERT OR DELETE OR TRUNCATE ON nodes
    FOR EACH STATEMENT EXECUTE FUNCTION bump_node_config_version();

CREATE TRIGGER node_config_version_nodes_profile
    AFTER UPDATE OF name, inbound_tags, listen_ports, core_overrides ON nodes
    FOR EACH STATEMENT EXECUTE FUNCTION bump_node_config_version();

-- +goose Down
DROP TRIGGER node_config_version_nodes_profile ON nodes;
DROP TRIGGER node_config_version_nodes_rows ON nodes;
DROP TRIGGER node_config_version_users_identity ON users;
DROP TRIGGER node_config_version_users_rows ON users;
DROP TRIGGER node_config_version_proxies ON proxies;
DROP TRIGGER node_config_version_core_config ON core_config;
DROP TRIGGER node_config_version_inbounds ON inbounds;
DROP TRIGGER node_config_version_hosts ON hosts;
DROP FUNCTION bump_node_config_version();
DROP FUNCTION bump_data_version(TEXT);
DROP TABLE data_versions;
ALTER TABLE nodes
    DROP COLUMN core_overrides,
    DROP COLUMN listen_ports,
    DROP COLUMN inbound_tags;
