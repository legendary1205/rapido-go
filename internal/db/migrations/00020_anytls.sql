-- +goose Up
-- Phase: sing-box protocol support, inbound slice 3 - adds AnyTLS as a real
-- proxy protocol type, alongside vmess/vless/trojan/shadowsocks/hysteria2/
-- tuic/snell. AnyTLS reuses the existing tls_certificate/tls_key/
-- tls_server_name columns exactly like the TCP-family protocols already do
-- (see internal/nodecore/anytls's own doc comment for why TLS is mandatory
-- here) - no new columns needed at all, unlike hysteria2/tuic/snell.
ALTER TABLE inbounds DROP CONSTRAINT inbounds_protocol_check;
ALTER TABLE inbounds ADD CONSTRAINT inbounds_protocol_check
    CHECK (protocol IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic', 'snell', 'anytls'));

ALTER TABLE proxies DROP CONSTRAINT proxies_type_check;
ALTER TABLE proxies ADD CONSTRAINT proxies_type_check
    CHECK (type IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic', 'snell', 'anytls'));

-- +goose Down
ALTER TABLE proxies DROP CONSTRAINT proxies_type_check;
ALTER TABLE proxies ADD CONSTRAINT proxies_type_check
    CHECK (type IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic', 'snell'));

ALTER TABLE inbounds DROP CONSTRAINT inbounds_protocol_check;
ALTER TABLE inbounds ADD CONSTRAINT inbounds_protocol_check
    CHECK (protocol IN ('vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria2', 'tuic', 'snell'));
