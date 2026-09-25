-- name: DeleteOldNodeUserUsages :execrows
-- One bounded slice of the hourly retention sweep (internal/usagejob). The
-- primary-key sub-select with a LIMIT keeps each statement's lock time and
-- WAL burst small on a table that grows by ~100k rows/day, and the
-- (created_at, user_id, node_id) unique index serves the created_at range.
DELETE FROM node_user_usages
WHERE id IN (
    SELECT id FROM node_user_usages
    WHERE created_at < sqlc.arg('cutoff')::timestamptz
    LIMIT sqlc.arg('batch_size')::int
);
