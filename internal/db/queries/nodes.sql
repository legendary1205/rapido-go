-- name: CreateNode :one
INSERT INTO nodes (name, address, port, api_port, usage_coefficient)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetNodeByID :one
SELECT * FROM nodes WHERE id = $1;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY id;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = $1;
