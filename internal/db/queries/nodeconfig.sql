-- name: ListAutoSyncInbounds :many
-- Inbounds eligible for automatic node-config generation (see
-- internal/httpapi/nodeconfig.go): only 'none' and 'reality' security
-- modes are fully self-contained in this schema already - a 'tls' inbound
-- needs a certificate/key this rewrite has nowhere to store yet (a
-- separate, pre-existing gap, not something this phase's scope covers),
-- so it's excluded here and stays on the existing manual POST /start path
-- untouched. The primary host (lowest id, not disabled, with a real port)
-- supplies the actual listen port - Hosts can carry several rows per
-- inbound tag for client-facing branding, but a raw inbound listener has
-- exactly one real port, same limitation the Xray config this data model
-- is descended from already has.
SELECT i.tag, i.protocol, i.network, i.header_type, i.security,
       i.reality_private_key, i.reality_short_ids, i.reality_server_name, i.reality_server_port,
       h.port, h.sni, h.host
FROM inbounds i
JOIN LATERAL (
    SELECT port, sni, host FROM hosts
    WHERE hosts.inbound_tag = i.tag AND is_disabled IS NOT TRUE AND port IS NOT NULL
    ORDER BY id LIMIT 1
) h ON true
WHERE i.security IN ('none', 'reality')
ORDER BY i.tag;

-- name: ListActiveUserProxiesForNodeConfig :many
-- Every active/on_hold user's proxy credentials, for grouping by protocol
-- into each inbound's user list - the Go equivalent of
-- XRayConfig.include_db_users(). A disabled/limited/expired user is
-- deliberately excluded so a node never even receives credentials it
-- shouldn't accept traffic for.
SELECT u.id, u.username, p.type, p.settings
FROM users u
JOIN proxies p ON p.user_id = u.id
WHERE u.status IN ('active', 'on_hold')
ORDER BY u.id;
