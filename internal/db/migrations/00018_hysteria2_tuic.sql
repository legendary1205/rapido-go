-- +goose Up
-- Phase: sing-box protocol support, inbound slice 1 - adds hysteria2 and
-- tuic as real proxy protocol types a user can be given and an inbound can
-- serve, alongside the existing vmess/vless/trojan/shadowsocks. Both are
-- QUIC-based (TLS is mandatory - see internal/nodecore/hysteria2 and
-- internal/nodecore/tuic's own doc comments), so neither needs the
-- reality_*/network/header_type columns this table already carries for the
-- TCP-family protocols; they get their own few nullable columns instead,
-- same "one wide table, protocol-specific columns nullable everywhere else"
-- convention 00004/00009 already established for reality_*/tls_*.
ALTER TABLE inbounds DROP CONSTRAINT inbounds_protocol_check;
ALTER TABLE inbounds ADD CONSTRAINT inbounds_protocol_check
    CHECK (protocol IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic'));

ALTER TABLE proxies DROP CONSTRAINT proxies_type_check;
ALTER TABLE proxies ADD CONSTRAINT proxies_type_check
    CHECK (type IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic'));

-- hysteria2 obfuscation (optional - salamander only, matching Core Config's
-- own outbound side which exposes no obfs at all yet) and the server's own
-- declared bandwidth (also optional: unlike Hysteria v1, hysteria2 defaults
-- to BBR and only uses these as a hint - see internal/nodecore/hysteria2's
-- doc comment for why this column does NOT get a NOT NULL/positive check
-- the way Core Config's outbound-side up_mbps/down_mbps required for v1
-- does).
ALTER TABLE inbounds ADD COLUMN hysteria2_obfs_password TEXT;
ALTER TABLE inbounds ADD COLUMN up_mbps INTEGER;
ALTER TABLE inbounds ADD COLUMN down_mbps INTEGER;

-- tuic's own inbound-level knobs - congestion_control mirrors Core Config's
-- outbound-side field/values (internal/httpapi/coreconfig.go's
-- validCongestionControl); empty/NULL means the node's own "cubic" default.
ALTER TABLE inbounds ADD COLUMN congestion_control TEXT
    CHECK (congestion_control IS NULL OR congestion_control IN ('cubic', 'new_reno', 'bbr'));
ALTER TABLE inbounds ADD COLUMN zero_rtt_handshake BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE inbounds DROP COLUMN zero_rtt_handshake;
ALTER TABLE inbounds DROP COLUMN congestion_control;
ALTER TABLE inbounds DROP COLUMN down_mbps;
ALTER TABLE inbounds DROP COLUMN up_mbps;
ALTER TABLE inbounds DROP COLUMN hysteria2_obfs_password;

ALTER TABLE proxies DROP CONSTRAINT proxies_type_check;
ALTER TABLE proxies ADD CONSTRAINT proxies_type_check
    CHECK (type IN ('vmess', 'vless', 'trojan', 'shadowsocks'));

ALTER TABLE inbounds DROP CONSTRAINT inbounds_protocol_check;
ALTER TABLE inbounds ADD CONSTRAINT inbounds_protocol_check
    CHECK (protocol IN ('vmess', 'vless', 'trojan', 'shadowsocks'));
