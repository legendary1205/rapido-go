-- +goose Up
-- Global, cross-inbound-tag display/priority order for hosts - see the
-- "global host order" plan (2026-09-07): the admin needs to interleave
-- configs from *different* inbound tags/nodes in one chosen sequence, not
-- just reorder within a single tag. Previously there was no ordering
-- control at all: ListHosts/ListHostsByInboundTag sorted by `id`
-- (creation order) and the outer inbound-tag loop in
-- internal/httpapi/subscription.go's forEachUserHost sorted tags
-- alphabetically - neither was admin-controlled.
--
-- Backfilled with the fleet's current effective order (tag alphabetical,
-- then id within a tag) as the starting point, so existing subscription
-- link order doesn't jumble on deploy - the admin then rearranges from
-- there using the new UI.
ALTER TABLE hosts ADD COLUMN priority INTEGER;

UPDATE hosts SET priority = sub.rn
FROM (
    SELECT id, ROW_NUMBER() OVER (ORDER BY inbound_tag, id) AS rn FROM hosts
) sub
WHERE hosts.id = sub.id;

ALTER TABLE hosts ALTER COLUMN priority SET NOT NULL;
ALTER TABLE hosts ALTER COLUMN priority SET DEFAULT 0;
CREATE INDEX hosts_priority_idx ON hosts (priority, id);

-- +goose Down
DROP INDEX IF EXISTS hosts_priority_idx;
ALTER TABLE hosts DROP COLUMN priority;
