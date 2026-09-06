-- name: CreateUserTemplate :one
INSERT INTO user_templates (name, data_limit, expire_duration, username_prefix, username_suffix)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetUserTemplateByID :one
SELECT * FROM user_templates WHERE id = $1;

-- name: ListUserTemplates :many
SELECT * FROM user_templates ORDER BY id;

-- name: UpdateUserTemplate :one
UPDATE user_templates SET
    name = $2,
    data_limit = $3,
    expire_duration = $4,
    username_prefix = $5,
    username_suffix = $6
WHERE id = $1
RETURNING *;

-- name: DeleteUserTemplate :exec
DELETE FROM user_templates WHERE id = $1;

-- name: ReplaceTemplateInbounds :exec
INSERT INTO template_inbounds_association (user_template_id, inbound_tag)
SELECT $1, unnest($2::text[]);

-- name: DeleteTemplateInbounds :exec
DELETE FROM template_inbounds_association WHERE user_template_id = $1;

-- name: ListTemplateInboundTags :many
SELECT inbound_tag FROM template_inbounds_association WHERE user_template_id = $1;
