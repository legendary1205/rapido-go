-- +goose Up

-- How many open CLIENT connections (what presence:node:<id>:total counts) mean
-- 100% load for this node. NULL = use the panel-wide default
-- (CONFIG_LOAD_CAPACITY). The column is deliberately not in the column list of
-- the node_config_version_nodes_profile trigger (migration 00015): the capacity
-- only feeds the load indicator and never changes what a node is told to run, so
-- editing it must not make every node re-download and re-apply its config.
ALTER TABLE nodes
    ADD COLUMN capacity INTEGER CHECK (capacity IS NULL OR capacity > 0);

-- Open client connections at the moment a node reported (its exact presence
-- total, as opposed to `connections`, which counts every established TCP socket
-- on the host, including the node's own upstream ones). NULL for a node that
-- does not send it and for the panel's own sample.
ALTER TABLE host_metrics
    ADD COLUMN client_conns INTEGER;

-- +goose Down
ALTER TABLE host_metrics DROP COLUMN client_conns;
ALTER TABLE nodes DROP COLUMN capacity;
