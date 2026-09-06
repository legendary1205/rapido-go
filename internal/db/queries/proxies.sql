-- name: CreateProxy :one
INSERT INTO proxies (user_id, type, settings) VALUES ($1, $2, $3)
RETURNING *;

-- name: ListProxiesByUserID :many
SELECT * FROM proxies WHERE user_id = $1 ORDER BY id;

-- name: DeleteProxyByID :exec
DELETE FROM proxies WHERE id = $1;

-- name: DeleteProxiesByUserID :exec
-- The composite PK added to exclude_inbounds_association (00001_init_schema.sql)
-- removes the StaleDataError risk that made the current Python code delete
-- and recreate proxies through raw Core statements instead of a normal
-- ORM cascade - a plain delete+recreate on update is safe here.
DELETE FROM proxies WHERE user_id = $1;

-- name: ReplaceExcludedInbounds :exec
INSERT INTO exclude_inbounds_association (proxy_id, inbound_tag)
SELECT $1, unnest($2::text[]);

-- name: DeleteExcludedInbounds :exec
DELETE FROM exclude_inbounds_association WHERE proxy_id = $1;

-- name: ListExcludedInboundTags :many
SELECT inbound_tag FROM exclude_inbounds_association WHERE proxy_id = $1 ORDER BY inbound_tag;

-- name: UpdateProxySettings :one
UPDATE proxies SET settings = $2 WHERE id = $1
RETURNING *;
