-- name: GetJWTSecret :one
SELECT * FROM jwt_secrets ORDER BY id LIMIT 1;

-- name: CreateJWTSecret :one
INSERT INTO jwt_secrets (secret_key) VALUES ($1)
RETURNING *;

-- name: ReplaceJWTSecret :exec
-- Used only by a legacy-panel import, to adopt the SOURCE panel's signing
-- key. Without it every subscription link already installed in a customer's
-- client app stops validating the moment the migration completes.
INSERT INTO jwt_secrets (id, secret_key) VALUES (1, $1)
ON CONFLICT (id) DO UPDATE SET secret_key = EXCLUDED.secret_key;
