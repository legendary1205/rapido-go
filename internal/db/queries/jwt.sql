-- name: GetJWTSecret :one
SELECT * FROM jwt_secrets ORDER BY id LIMIT 1;

-- name: CreateJWTSecret :one
INSERT INTO jwt_secrets (secret_key) VALUES ($1)
RETURNING *;
