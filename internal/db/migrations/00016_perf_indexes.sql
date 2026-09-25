-- +goose Up

-- Every node report rewrites users.used_traffic and users.online_at for each
-- online user. While online_at is indexed Postgres cannot do a HOT update, so
-- each of those rewrites also rewrites an index entry: measured in production
-- as a 46 MB index over a 1.7 MB heap and ~129 GB written in 12 days. The
-- "online users" count filters on online_at but scans 9.4k rows in ~1 ms
-- without the index, and no other query filters on either column
-- (users_expire_idx: 1 scan in 12 days; the review job's expire predicate is
-- OR-ed with an unindexed data_limit branch, so it never used it either).
DROP INDEX IF EXISTS users_online_at_idx;
DROP INDEX IF EXISTS users_expire_idx;

-- Leave free space in each page so the (now HOT-eligible) updates stay on the
-- same page, and vacuum the two write-heavy tables after 2% churn instead of
-- the 20% default, which on a 1M-row table means 200k dead tuples. This only
-- shapes future pages; reclaiming existing bloat is a deploy-time
-- REINDEX/VACUUM, deliberately not a table rewrite inside a migration.
ALTER TABLE users SET (
    fillfactor = 85,
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_analyze_scale_factor = 0.02
);
ALTER TABLE node_user_usages SET (
    fillfactor = 90,
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_analyze_scale_factor = 0.02
);

-- +goose Down
ALTER TABLE node_user_usages RESET (
    fillfactor,
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor
);
ALTER TABLE users RESET (
    fillfactor,
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor
);
CREATE INDEX IF NOT EXISTS users_expire_idx ON users (expire);
CREATE INDEX IF NOT EXISTS users_online_at_idx ON users (online_at);
