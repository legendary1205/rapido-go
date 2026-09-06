-- name: UpsertNextPlan :one
INSERT INTO next_plans (user_id, data_limit, expire, add_remaining_traffic, fire_on_either)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id) DO UPDATE SET
    data_limit = EXCLUDED.data_limit,
    expire = EXCLUDED.expire,
    add_remaining_traffic = EXCLUDED.add_remaining_traffic,
    fire_on_either = EXCLUDED.fire_on_either
RETURNING *;

-- name: DeleteNextPlanByUserID :exec
DELETE FROM next_plans WHERE user_id = $1;

-- name: GetNextPlanByUserID :one
SELECT * FROM next_plans WHERE user_id = $1;

-- name: GetNextPlansByUserIDs :many
-- Batched form of GetNextPlanByUserID for GET /api/users.
SELECT * FROM next_plans WHERE user_id = ANY($1::int[]);
