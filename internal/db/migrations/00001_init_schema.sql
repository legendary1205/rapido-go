-- +goose Up

-- admins ---------------------------------------------------------------
CREATE TABLE admins (
    id                  SERIAL PRIMARY KEY,
    username            VARCHAR(34) NOT NULL,
    hashed_password     VARCHAR(128) NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_sudo             BOOLEAN NOT NULL DEFAULT false,
    password_reset_at   TIMESTAMPTZ,
    telegram_id         BIGINT,
    discord_webhook     VARCHAR(1024),
    users_usage         BIGINT NOT NULL DEFAULT 0
);
-- Case-sensitive, matching production MySQL's utf8mb4_bin collation (not
-- dev SQLite's NOCASE) - "Admin" and "admin" are different usernames.
CREATE UNIQUE INDEX admins_username_key ON admins (username);

-- inbounds ---------------------------------------------------------------
CREATE TABLE inbounds (
    id      SERIAL PRIMARY KEY,
    tag     VARCHAR(256) NOT NULL
);
CREATE UNIQUE INDEX inbounds_tag_key ON inbounds (tag);

-- users ------------------------------------------------------------------
CREATE TABLE users (
    id                          SERIAL PRIMARY KEY,
    username                    VARCHAR(34) NOT NULL,
    status                      TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'disabled', 'limited', 'expired', 'on_hold')),
    used_traffic                BIGINT NOT NULL DEFAULT 0,
    data_limit                  BIGINT,
    data_limit_reset_strategy   TEXT NOT NULL DEFAULT 'no_reset'
        CHECK (data_limit_reset_strategy IN ('no_reset', 'day', 'week', 'month', 'year')),
    expire                      INTEGER,
    -- Explicit RESTRICT: undefined at the DB level today (no cascade
    -- declared anywhere in the current MySQL schema), which would hard-fail
    -- rather than silently orphan users if an admin row were ever deleted.
    admin_id                    INTEGER REFERENCES admins (id) ON DELETE RESTRICT,
    sub_revoked_at              TIMESTAMPTZ,
    sub_updated_at              TIMESTAMPTZ,
    sub_last_user_agent         VARCHAR(512),
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    note                        VARCHAR(500),
    online_at                   TIMESTAMPTZ,
    on_hold_expire_duration     BIGINT,
    on_hold_timeout             TIMESTAMPTZ,
    -- Positive: user auto-deletes after this many days. Negative: never
    -- auto-deletes. NULL: use global settings.
    auto_delete_in_days         INTEGER,
    edit_at                     TIMESTAMPTZ,
    last_status_change          TIMESTAMPTZ DEFAULT now(),
    -- NULL = the one-time self-service emergency recharge has never been used.
    emergency_used_at           TIMESTAMPTZ
);
CREATE UNIQUE INDEX users_username_key ON users (username);
CREATE INDEX users_status_idx ON users (status);
CREATE INDEX users_expire_idx ON users (expire);
CREATE INDEX users_admin_id_idx ON users (admin_id);
-- count_online_users runs on every dashboard poll; without this it's a full
-- table scan each time.
CREATE INDEX users_online_at_idx ON users (online_at);

-- proxies ------------------------------------------------------------------
CREATE TABLE proxies (
    id          SERIAL PRIMARY KEY,
    user_id     INTEGER REFERENCES users (id) ON DELETE CASCADE,
    type        TEXT NOT NULL
        CHECK (type IN ('vmess', 'vless', 'trojan', 'shadowsocks')),
    settings    JSONB NOT NULL
);
CREATE INDEX proxies_user_id_idx ON proxies (user_id);

-- hosts --------------------------------------------------------------------
CREATE TABLE hosts (
    id                  SERIAL PRIMARY KEY,
    remark              VARCHAR(256) NOT NULL,
    address             VARCHAR(256) NOT NULL,
    port                INTEGER,
    path                VARCHAR(256),
    sni                 VARCHAR(1000),
    host                VARCHAR(1000),
    security            TEXT NOT NULL DEFAULT 'inbound_default'
        CHECK (security IN ('inbound_default', 'none', 'tls')),
    -- Fixed: the original SQLAlchemy default/server_default referenced
    -- ProxyHostSecurity.none instead of this column's own enum's "none"
    -- member. Stored as the canonical identifier; "none" maps to the
    -- empty-string wire value when building an xray/sing-box config.
    alpn                TEXT NOT NULL DEFAULT 'none'
        CHECK (alpn IN ('none', 'h3', 'h2', 'http/1.1', 'h3,h2,http/1.1', 'h3,h2', 'h2,http/1.1')),
    fingerprint         TEXT NOT NULL DEFAULT 'none'
        CHECK (fingerprint IN ('none', 'chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized')),
    inbound_tag         VARCHAR(256) NOT NULL REFERENCES inbounds (tag) ON DELETE CASCADE,
    allowinsecure       BOOLEAN,
    is_disabled         BOOLEAN DEFAULT false,
    mux_enable          BOOLEAN NOT NULL DEFAULT false,
    fragment_setting    VARCHAR(100),
    noise_setting       VARCHAR(2000),
    random_user_agent   BOOLEAN NOT NULL DEFAULT false,
    use_sni_as_host     BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX hosts_inbound_tag_idx ON hosts (inbound_tag);

-- exclude_inbounds_association ----------------------------------------------
-- Fixed: the original join table had no primary key at all, which let a
-- StaleDataError reach production (fixed there for this table's sibling,
-- template_inbounds_association, by migration a1e3f8c2b9d4 - this table
-- never got the same fix in MySQL). Cascades on both sides so a deleted
-- proxy or inbound cleans up its exclusion rows instead of leaving them
-- orphaned or blocking the delete.
CREATE TABLE exclude_inbounds_association (
    proxy_id      INTEGER NOT NULL REFERENCES proxies (id) ON DELETE CASCADE,
    inbound_tag   VARCHAR(256) NOT NULL REFERENCES inbounds (tag) ON DELETE CASCADE,
    PRIMARY KEY (proxy_id, inbound_tag)
);

-- user_templates -------------------------------------------------------------
CREATE TABLE user_templates (
    id                  SERIAL PRIMARY KEY,
    name                VARCHAR(64) NOT NULL,
    data_limit          BIGINT DEFAULT 0,
    expire_duration     BIGINT DEFAULT 0, -- seconds
    username_prefix     VARCHAR(20),
    username_suffix     VARCHAR(20)
);
CREATE UNIQUE INDEX user_templates_name_key ON user_templates (name);

-- template_inbounds_association -----------------------------------------------
CREATE TABLE template_inbounds_association (
    user_template_id   INTEGER NOT NULL REFERENCES user_templates (id) ON DELETE CASCADE,
    inbound_tag         VARCHAR(256) NOT NULL REFERENCES inbounds (tag) ON DELETE CASCADE,
    PRIMARY KEY (user_template_id, inbound_tag)
);

-- next_plans -------------------------------------------------------------------
CREATE TABLE next_plans (
    id                      SERIAL PRIMARY KEY,
    user_id                 INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    data_limit              BIGINT NOT NULL,
    expire                  INTEGER,
    add_remaining_traffic   BOOLEAN NOT NULL DEFAULT false,
    -- Fixed: the original Python-side default (True) and DB server_default
    -- ('0' / false) disagreed - rows created by raw SQL/migrations got a
    -- different default than rows created through the ORM. This is the
    -- intended value everywhere (see app/models/user.py's NextPlanModel).
    fire_on_either          BOOLEAN NOT NULL DEFAULT true
);
CREATE UNIQUE INDEX next_plans_user_id_key ON next_plans (user_id);

-- user_usage_logs ------------------------------------------------------------
CREATE TABLE user_usage_logs (
    id                      SERIAL PRIMARY KEY,
    user_id                 INTEGER REFERENCES users (id),
    used_traffic_at_reset   BIGINT NOT NULL,
    reset_at                TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX user_usage_logs_user_id_idx ON user_usage_logs (user_id);

-- admin_usage_logs -------------------------------------------------------------
CREATE TABLE admin_usage_logs (
    id                      SERIAL PRIMARY KEY,
    admin_id                INTEGER REFERENCES admins (id),
    used_traffic_at_reset   BIGINT NOT NULL,
    reset_at                TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX admin_usage_logs_admin_id_idx ON admin_usage_logs (admin_id);

-- system -------------------------------------------------------------------------
CREATE TABLE system (
    id          SERIAL PRIMARY KEY,
    uplink      BIGINT DEFAULT 0,
    downlink    BIGINT DEFAULT 0
);

-- jwt_secrets ----------------------------------------------------------------------
-- Singleton table (one row) holding the HMAC secret used to sign every JWT
-- this panel issues - equivalent to the current "jwt" table.
CREATE TABLE jwt_secrets (
    id          SERIAL PRIMARY KEY,
    secret_key  VARCHAR(64) NOT NULL
);

-- tls ------------------------------------------------------------------------------
-- Singleton table holding the Rapido-branded CA/cert material (see
-- internal/certs) used for panel<->node mTLS.
CREATE TABLE tls (
    id            SERIAL PRIMARY KEY,
    key           TEXT NOT NULL,
    certificate   TEXT NOT NULL
);

-- integration_settings -------------------------------------------------------------
-- Sudo-editable overrides for integration config normally read once from
-- env at startup. NULL in any column means "use the env value".
CREATE TABLE integration_settings (
    id                              SERIAL PRIMARY KEY,
    kirbot_secret                   VARCHAR(256),
    kirbot_url                      VARCHAR(512),
    kirbot_license                  VARCHAR(256),
    telegram_api_token              VARCHAR(256),
    telegram_admin_ids              JSONB,
    telegram_proxy_url              VARCHAR(512),
    telegram_logger_channel_id      BIGINT,
    telegram_logger_topic_id        BIGINT,
    telegram_default_vless_flow     VARCHAR(64),
    webhook_addresses               JSONB,
    webhook_secret                  VARCHAR(256),
    discord_webhook_url             VARCHAR(1024),
    updated_at                      TIMESTAMPTZ
);

-- nodes --------------------------------------------------------------------------------
CREATE TABLE nodes (
    id                    SERIAL PRIMARY KEY,
    name                  VARCHAR(256) NOT NULL,
    address               VARCHAR(256) NOT NULL,
    port                  INTEGER NOT NULL,
    api_port              INTEGER NOT NULL,
    xray_version          VARCHAR(32),
    status                TEXT NOT NULL DEFAULT 'connecting'
        CHECK (status IN ('connected', 'connecting', 'error', 'disabled')),
    last_status_change    TIMESTAMPTZ DEFAULT now(),
    message               VARCHAR(1024),
    created_at            TIMESTAMPTZ DEFAULT now(),
    uplink                BIGINT DEFAULT 0,
    downlink              BIGINT DEFAULT 0,
    usage_coefficient     DOUBLE PRECISION NOT NULL DEFAULT 1.0
);
-- Case-sensitive, matching production MySQL (see admins.username above).
CREATE UNIQUE INDEX nodes_name_key ON nodes (name);

-- node_user_usages -----------------------------------------------------------------------
-- BIGSERIAL: high-volume table (one row per user per node per hour).
CREATE TABLE node_user_usages (
    id            BIGSERIAL PRIMARY KEY,
    created_at    TIMESTAMPTZ NOT NULL, -- one hour per record
    user_id       INTEGER REFERENCES users (id) ON DELETE CASCADE,
    node_id       INTEGER REFERENCES nodes (id) ON DELETE CASCADE,
    used_traffic  BIGINT DEFAULT 0,
    UNIQUE (created_at, user_id, node_id)
);

-- node_usages ----------------------------------------------------------------------------
-- BIGSERIAL: high-volume table (one row per node per hour).
CREATE TABLE node_usages (
    id            BIGSERIAL PRIMARY KEY,
    created_at    TIMESTAMPTZ NOT NULL, -- one hour per record
    node_id       INTEGER REFERENCES nodes (id) ON DELETE CASCADE,
    uplink        BIGINT DEFAULT 0,
    downlink      BIGINT DEFAULT 0,
    UNIQUE (created_at, node_id)
);

-- host_metrics ---------------------------------------------------------------------------
-- One monitoring sample from one machine. node_id NULL means the panel
-- itself, the same convention the usage tables use for the local core, so
-- a single query covers the whole fleet. BIGSERIAL: high-volume table (one
-- row per host roughly every 30s).
CREATE TABLE host_metrics (
    id              BIGSERIAL PRIMARY KEY,
    node_id         INTEGER REFERENCES nodes (id) ON DELETE CASCADE,
    collected_at    TIMESTAMPTZ NOT NULL,
    cpu_percent     DOUBLE PRECISION,
    mem_percent     DOUBLE PRECISION,
    disk_percent    DOUBLE PRECISION,
    -- Bytes/sec since the previous sample, computed by the collector - the
    -- node reports cumulative counters and only the collector knows the gap.
    rx_rate         BIGINT,
    tx_rate         BIGINT,
    connections     INTEGER,
    tunnels_up      INTEGER,
    tunnels_total   INTEGER,
    healthy         BOOLEAN NOT NULL DEFAULT true,
    payload         TEXT
);
CREATE INDEX host_metrics_node_id_idx ON host_metrics (node_id);
CREATE INDEX host_metrics_collected_at_idx ON host_metrics (collected_at);

-- notification_reminders -----------------------------------------------------------------
CREATE TABLE notification_reminders (
    id            SERIAL PRIMARY KEY,
    user_id       INTEGER REFERENCES users (id) ON DELETE CASCADE,
    type          TEXT NOT NULL
        CHECK (type IN ('expiration_date', 'data_usage')),
    threshold     INTEGER,
    expires_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX notification_reminders_user_id_idx ON notification_reminders (user_id);

-- tickets --------------------------------------------------------------------------------
CREATE TABLE tickets (
    id            SERIAL PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subject       VARCHAR(255) NOT NULL,
    status        TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'closed')),
    created_at    TIMESTAMPTZ DEFAULT now(),
    -- Indexed because the admin inbox sorts by this (most recently active
    -- ticket first), not by created_at.
    updated_at    TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX tickets_user_id_idx ON tickets (user_id);
CREATE INDEX tickets_status_idx ON tickets (status);
CREATE INDEX tickets_updated_at_idx ON tickets (updated_at);

-- ticket_messages ------------------------------------------------------------------------
CREATE TABLE ticket_messages (
    id            SERIAL PRIMARY KEY,
    ticket_id     INTEGER NOT NULL REFERENCES tickets (id) ON DELETE CASCADE,
    is_admin      BOOLEAN NOT NULL DEFAULT false,
    body          TEXT NOT NULL,
    created_at    TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX ticket_messages_ticket_id_idx ON ticket_messages (ticket_id);
CREATE INDEX ticket_messages_created_at_idx ON ticket_messages (created_at);

-- +goose Down
DROP TABLE IF EXISTS ticket_messages;
DROP TABLE IF EXISTS tickets;
DROP TABLE IF EXISTS notification_reminders;
DROP TABLE IF EXISTS host_metrics;
DROP TABLE IF EXISTS node_usages;
DROP TABLE IF EXISTS node_user_usages;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS integration_settings;
DROP TABLE IF EXISTS tls;
DROP TABLE IF EXISTS jwt_secrets;
DROP TABLE IF EXISTS system;
DROP TABLE IF EXISTS admin_usage_logs;
DROP TABLE IF EXISTS user_usage_logs;
DROP TABLE IF EXISTS next_plans;
DROP TABLE IF EXISTS template_inbounds_association;
DROP TABLE IF EXISTS user_templates;
DROP TABLE IF EXISTS exclude_inbounds_association;
DROP TABLE IF EXISTS hosts;
DROP TABLE IF EXISTS proxies;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS inbounds;
DROP TABLE IF EXISTS admins;
