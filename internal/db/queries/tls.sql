-- name: GetTLS :one
SELECT * FROM tls ORDER BY id LIMIT 1;

-- name: CreateTLS :one
INSERT INTO tls (key, certificate) VALUES ($1, $2)
RETURNING *;
