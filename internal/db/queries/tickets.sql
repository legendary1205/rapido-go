-- name: CreateTicket :one
INSERT INTO tickets (user_id, subject) VALUES ($1, $2) RETURNING *;

-- name: CreateTicketMessage :one
INSERT INTO ticket_messages (ticket_id, is_admin, body) VALUES ($1, $2, $3) RETURNING *;

-- name: CountOpenTicketsByUserID :one
SELECT count(*) FROM tickets WHERE user_id = $1 AND status = 'open';

-- name: CountTicketMessages :one
SELECT count(*) FROM ticket_messages WHERE ticket_id = $1;

-- name: GetUserTicketByID :one
-- Ownership check baked into the WHERE - a foreign ticket_id is
-- indistinguishable from a nonexistent one, matching Python's
-- get_user_ticket (no ownership leak via a 403 vs 404 distinction).
SELECT * FROM tickets WHERE id = $1 AND user_id = $2;

-- name: ListUserTickets :many
SELECT * FROM tickets WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3;

-- name: ListTicketMessagesByTicketIDs :many
-- Batched form, avoiding one query per ticket when assembling a page of
-- tickets each with their own message thread.
SELECT * FROM ticket_messages WHERE ticket_id = ANY($1::int[]) ORDER BY ticket_id, id;

-- name: TouchAndReopenTicket :one
-- Called after inserting a message from either side - mirrors
-- add_ticket_message's exact behavior: updated_at always bumps (so the
-- ticket jumps to the top of the inbox), and a closed ticket always
-- reopens on any reply, customer or admin. Setting status='open'
-- unconditionally is a harmless no-op when it was already open.
UPDATE tickets SET updated_at = now(), status = 'open' WHERE id = $1 RETURNING *;

-- name: UpdateTicketStatus :one
UPDATE tickets SET status = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: GetAdminTicketByID :one
-- admin_id NULL means unscoped (sudo sees every ticket); otherwise only
-- tickets whose owning customer's admin_id matches - mirrors
-- _ticket_owner_condition. Returns 0 rows (not an error) for a
-- foreign-reseller's ticket id, same not-found-vs-forbidden non-leak as
-- GetUserTicketByID above.
SELECT t.*, u.username AS owner_username, a.username AS owner_admin_username
FROM tickets t
JOIN users u ON u.id = t.user_id
LEFT JOIN admins a ON a.id = u.admin_id
WHERE t.id = $1 AND (sqlc.narg('admin_id')::int IS NULL OR u.admin_id = sqlc.narg('admin_id')::int);

-- name: ListAdminTickets :many
SELECT t.*, u.username AS owner_username, a.username AS owner_admin_username
FROM tickets t
JOIN users u ON u.id = t.user_id
LEFT JOIN admins a ON a.id = u.admin_id
WHERE (sqlc.narg('admin_id')::int IS NULL OR u.admin_id = sqlc.narg('admin_id')::int)
  AND (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status')::text)
ORDER BY t.updated_at DESC, t.id DESC
LIMIT sqlc.arg('limit_count')::int OFFSET sqlc.arg('offset_count')::int;

-- name: CountAdminTickets :one
SELECT count(*) FROM tickets t
JOIN users u ON u.id = t.user_id
WHERE (sqlc.narg('admin_id')::int IS NULL OR u.admin_id = sqlc.narg('admin_id')::int)
  AND (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status')::text);

-- name: ApplyEmergencyRecharge :one
-- Atomic single-statement equivalent of Python's claim-then-update
-- two-step (a conditional UPDATE followed by a second UPDATE) - Postgres
-- does the whole thing race-safely in one RETURNING statement: zero rows
-- back means another request already claimed the grant (or it was used
-- long ago), same signal as Python's rowcount==0 path.
UPDATE users SET
    emergency_used_at = now(),
    data_limit = CASE WHEN data_limit > 0 THEN data_limit + 52428800 ELSE data_limit END,
    expire = CASE WHEN expire > 0 THEN GREATEST(expire, extract(epoch FROM now())::int) + 1800 ELSE expire END,
    status = 'active',
    last_status_change = now()
WHERE id = $1 AND emergency_used_at IS NULL
RETURNING *;
