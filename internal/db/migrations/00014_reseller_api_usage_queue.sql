-- +goose Up
-- Per-admin traffic waiting to be reported to the external reseller bot
-- (POST {reseller_api_url}/api/subscriptions/{secret}/usages), which bills
-- each reseller's wallet from it. A node report adds to `pending` in the
-- same statement that raises admins.users_usage, and the backend job moves
-- whatever the bot accepted from `pending` to `reported`. Keeping the queue
-- in the database rather than in memory means a restart, a deploy or a bot
-- outage delays billing instead of losing it.
CREATE TABLE reseller_api_usage_queue (
    admin_id int PRIMARY KEY REFERENCES admins (id) ON DELETE CASCADE,
    pending  bigint NOT NULL DEFAULT 0,
    reported bigint NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE reseller_api_usage_queue;
