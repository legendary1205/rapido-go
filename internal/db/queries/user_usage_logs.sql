-- name: CreateUserUsageLog :exec
INSERT INTO user_usage_logs (user_id, used_traffic_at_reset) VALUES ($1, $2);
