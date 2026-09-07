-- +goose Up
-- Singleton row (same pattern as integration_settings/system) holding the
-- fleet-wide sing-box sections the current Python system's "Core Config"
-- feature covers that have no other home in this schema: custom outbounds,
-- routing rules, DNS servers, log level, and a default sniffing toggle.
-- Deliberately NOT here: Xray's "policy" and "fallback" sections have no
-- real sing-box equivalent (see the Phase 7.4 plan's context) - building
-- settings for them would control nothing real. Also deliberately just one
-- sniffing toggle, not two: sing-box 1.14's per-inbound
-- sniff_override_destination field is itself deprecated in favor of rule
-- actions, and the modern {"action":"sniff"} rule action has no matching
-- "override destination" knob at all - only sniff_enabled (translated into
-- an unconditional leading {"action":"sniff"} routing rule) has a real,
-- current equivalent.
CREATE TABLE core_config (
    id                          SERIAL PRIMARY KEY,
    log_level                   TEXT NOT NULL DEFAULT 'warn'
        CHECK (log_level IN ('trace', 'debug', 'info', 'warn', 'error', 'fatal', 'panic')),
    sniff_enabled               BOOLEAN NOT NULL DEFAULT true,
    -- Each: {"tag": "...", "type": "direct|block|socks|http|selector|urltest", ...type-specific fields}
    outbounds                   JSONB NOT NULL DEFAULT '[]',
    -- Each: {match criteria fields..., "outbound_tag": "..."}
    routing_rules                JSONB NOT NULL DEFAULT '[]',
    -- Each: {"tag": "...", "type": "local|udp|tcp|tls|https", "address": "...", "port": N, "path": "..."}
    dns_servers                  JSONB NOT NULL DEFAULT '[]',
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO core_config DEFAULT VALUES;

-- +goose Down
DROP TABLE core_config;
