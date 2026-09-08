-- +goose Up
-- Gateway sub-phase 2 (user sync). Replaces users.synced_from_peer_id
-- (added in 00010) with a plain self-reported label instead of a real FK
-- to gateway_peers - a design gap caught while actually building the sync
-- endpoint: every peer authenticates to this panel with the SAME shared
-- gateway_settings.secret (see 00010's own doc comment on why that's
-- asymmetric/per-installation), so a receiving panel has no way to
-- resolve an inbound sync call back to one specific local gateway_peers
-- row - it may not even have one (nothing requires the relationship to be
-- configured in both directions). A self-reported name is enough for what
-- this column is actually for: flagging a user as a replica (so the
-- owning panel's UI makes it read-only and the sync hook never re-syncs
-- it back out) and showing a human-recognizable label - it was never used
-- as a real relational join target.
ALTER TABLE users DROP COLUMN synced_from_peer_id;
ALTER TABLE users ADD COLUMN synced_from_panel_name TEXT;

-- +goose Down
ALTER TABLE users DROP COLUMN synced_from_panel_name;
ALTER TABLE users ADD COLUMN synced_from_peer_id INTEGER
    REFERENCES gateway_peers (id) ON DELETE SET NULL;
