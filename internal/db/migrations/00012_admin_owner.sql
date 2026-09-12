-- +goose Up
-- A tier above sudo: an owner can grant or revoke sudo access on any other
-- admin, including other sudo admins - something no sudo admin, including
-- one editing themselves, can do today (PUT /api/admin/{username} refuses
-- to touch another sudo account at all, and even self-edits can only ever
-- turn is_sudo on, never back off - see UpdateAdmin's own history). Plain
-- boolean, same shape as is_sudo itself, rather than a separate roles
-- table - there is exactly one more tier to model, not an open-ended set.
ALTER TABLE admins ADD COLUMN is_owner BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE admins DROP COLUMN is_owner;
