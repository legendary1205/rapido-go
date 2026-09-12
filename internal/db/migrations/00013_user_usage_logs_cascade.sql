-- +goose Up
-- user_usage_logs was the one table left behind when every other
-- user-owned table (proxies, next_plans, node_user_usages,
-- notification_reminders, tickets) got promoted from the ORM's
-- cascade="all, delete-orphan" to a real ON DELETE CASCADE FK (see
-- 00001_init_schema.sql's own history) - found live via
-- DELETE /api/user/:username failing with a plain 500 for any user who
-- had ever had usage recorded (a node report, or a manual reset), since
-- Postgres's default NO ACTION blocks the users row from being removed
-- while a user_usage_logs row still references it.
ALTER TABLE user_usage_logs DROP CONSTRAINT user_usage_logs_user_id_fkey;
ALTER TABLE user_usage_logs ADD CONSTRAINT user_usage_logs_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE user_usage_logs DROP CONSTRAINT user_usage_logs_user_id_fkey;
ALTER TABLE user_usage_logs ADD CONSTRAINT user_usage_logs_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users (id);
