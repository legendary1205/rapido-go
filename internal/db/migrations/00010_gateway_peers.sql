-- +goose Up
-- Multi-panel load balancer (Phase: Gateway) - lets an operator run several
-- fully independent rapido-go installs (own DB/admins each) and have a
-- user's subscription include hosts from every one of them, weighted by
-- real, live crowdedness. See the Gateway plan for the full design; this
-- migration is sub-phase 1 (peer plumbing + panel-to-panel auth) only.

-- This panel's own inbound secret - the bearer token any peer must present
-- when calling this panel's /api/internal/gateway/* endpoints. A dedicated
-- singleton table, not a column bolted onto integration_settings: this is
-- panel identity/security material generated once at boot if missing,
-- exactly like jwt_secrets and tls (see cmd/panel/main.go's
-- ensureJWTSecret/ensureTLS) - not a piece of third-party integration
-- config an admin fills in by hand.
-- `name` is this panel's own human-facing label, shown to a peer's admin
-- in a "Test connection" result so they can tell which install answered -
-- optional (defaults to empty), set from the same admin screen as the
-- secret.
CREATE TABLE gateway_settings (
    id         SERIAL PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    secret     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The peers THIS panel calls out to. Asymmetric and per-installation, not
-- per-pair: `secret` here is the OTHER panel's own gateway_settings.secret
-- (what this panel sends as its Bearer token when calling that peer) -
-- mirroring how each node gets its own independent report_secret rather
-- than a shared value negotiated per relationship.
CREATE TABLE gateway_peers (
    id         SERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    base_url   TEXT NOT NULL,
    secret     TEXT NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Marks a user as a replica mirrored in from a peer (via
-- POST /api/internal/gateway/users/sync) rather than a real local account -
-- see the Gateway plan's sub-phase 2. ON DELETE SET NULL: removing the
-- peer config shouldn't delete the mirrored users it created, just orphan
-- the marker (they stop being treated as synced-from-elsewhere).
ALTER TABLE users ADD COLUMN synced_from_peer_id INTEGER
    REFERENCES gateway_peers (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE users DROP COLUMN synced_from_peer_id;
DROP TABLE gateway_peers;
DROP TABLE gateway_settings;
