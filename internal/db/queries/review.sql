-- name: GetUsersNeedingStatusReview :many
-- Only users this job can actually act on - a faithful SQL translation of
-- review_users.py's `limited`/`expired` checks (zero/NULL semantics
-- included: a data_limit or expire of 0/NULL never triggers either
-- condition), pushed down to avoid loading every active user on every
-- tick. See that file's own comment: unfiltered, this was the slowest
-- single query in the whole system in production.
SELECT * FROM users
WHERE status = 'active'
  AND (
    (data_limit IS NOT NULL AND data_limit > 0 AND used_traffic >= data_limit)
    OR (expire IS NOT NULL AND expire != 0 AND expire <= $1)
  );

-- name: GetOnHoldUsers :many
SELECT * FROM users WHERE status = 'on_hold';

-- name: UpdateUserStatusOnly :exec
-- Mirrors crud.update_user_status exactly - distinct from UpdateUserCore
-- (used by the admin-facing PUT /user endpoint, which never touches
-- last_status_change) because only specific flows like this job stamp it.
UPDATE users SET status = $2, last_status_change = now() WHERE id = $1;

-- name: ActivateOnHoldUser :exec
-- Combines crud.update_user_status(active) + crud.start_user_expire into
-- one statement - both mutate the same row with no observable
-- intermediate state, so there's no reason to round-trip twice. Sets
-- expire = now + on_hold_expire_duration and clears both hold fields,
-- exactly like start_user_expire.
UPDATE users SET
    status = 'active',
    last_status_change = now(),
    expire = extract(epoch FROM now())::int + on_hold_expire_duration,
    on_hold_expire_duration = NULL,
    on_hold_timeout = NULL
WHERE id = $1;

-- name: ClearNodeUserUsages :exec
-- Mirrors crud._clear_node_usages - called by both the manual reset-usage
-- endpoint and NextPlan firing.
DELETE FROM node_user_usages WHERE user_id = $1;

-- name: ResetUserByNextPlan :one
-- Mirrors crud.reset_user_by_next's formula exactly, computed here in one
-- statement rather than read-then-write in Go: new data_limit = plan's
-- data_limit, plus the unused remainder of the old limit UNLESS
-- add_remaining_traffic discards it (see the Go rewrite's memory note
-- flagging this field's name as likely inverted from its actual effect -
-- ported as-is, not fixed, since nothing here changes that behavior).
UPDATE users u SET
    status = 'active',
    data_limit = np.data_limit + (CASE WHEN np.add_remaining_traffic THEN 0 ELSE u.data_limit - u.used_traffic END),
    expire = extract(epoch FROM now())::int + np.expire,
    used_traffic = 0
FROM next_plans np
WHERE u.id = $1 AND np.user_id = u.id
RETURNING u.*;
