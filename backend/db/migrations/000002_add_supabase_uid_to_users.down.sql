-- 000002_add_supabase_uid_to_users.down.sql
-- Rollback Supabase Auth UUID mapping

DROP INDEX IF EXISTS idx_users_supabase_uid;
ALTER TABLE users DROP COLUMN IF EXISTS supabase_uid;

