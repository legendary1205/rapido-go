-- +goose Up
-- Phase: sing-box protocol support, inbound slice 2 - adds Snell as a real
-- proxy protocol type a user can be given and an inbound can serve,
-- alongside vmess/vless/trojan/shadowsocks/hysteria2/tuic. Snell has no TLS
-- of its own (its own wire format does the obfuscation - see
-- internal/nodecore/snell's own doc comment), so it needs no
-- reality_*/tls_*/network columns this table already carries; it gets its
-- own two nullable columns instead, same convention 00004/00009/00018
-- already established.
ALTER TABLE inbounds DROP CONSTRAINT inbounds_protocol_check;
ALTER TABLE inbounds ADD CONSTRAINT inbounds_protocol_check
    CHECK (protocol IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic', 'snell'));

ALTER TABLE proxies DROP CONSTRAINT proxies_type_check;
ALTER TABLE proxies ADD CONSTRAINT proxies_type_check
    CHECK (type IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic', 'snell'));

-- snell_psk is the inbound-level pre-shared key EVERY user of that inbound
-- authenticates behind (a real per-user secret is a separate thing - see
-- proxies.settings' own "user_key" field) - required for a snell inbound,
-- nullable here because every other protocol's inbound row leaves it NULL,
-- same as tls_certificate etc. already do. sing-snell's own v6 server
-- requires 12-255 bytes; enforced in Go (internal/httpapi/inbounds.go), not
-- here, matching this table's existing convention of leaving length/shape
-- checks to the application layer.
ALTER TABLE inbounds ADD COLUMN snell_psk TEXT;
-- snell_v6_mode mirrors Core Config's own outbound-side V6Options.Mode
-- values (empty/NULL means the node's own "default" mode).
ALTER TABLE inbounds ADD COLUMN snell_v6_mode TEXT
    CHECK (snell_v6_mode IS NULL OR snell_v6_mode IN ('default', 'unshaped', 'unsafe-raw'));

-- +goose Down
ALTER TABLE inbounds DROP COLUMN snell_v6_mode;
ALTER TABLE inbounds DROP COLUMN snell_psk;

ALTER TABLE proxies DROP CONSTRAINT proxies_type_check;
ALTER TABLE proxies ADD CONSTRAINT proxies_type_check
    CHECK (type IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic'));

ALTER TABLE inbounds DROP CONSTRAINT inbounds_protocol_check;
ALTER TABLE inbounds ADD CONSTRAINT inbounds_protocol_check
    CHECK (protocol IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic'));
