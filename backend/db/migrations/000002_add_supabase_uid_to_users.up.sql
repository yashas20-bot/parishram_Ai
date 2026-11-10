-- 000002_add_supabase_uid_to_users.up.sql
-- Add Supabase Auth UUID mapping to users table without disturbing existing primary keys or data

ALTER TABLE users
ADD COLUMN IF NOT EXISTS supabase_uid UUID UNIQUE;

CREATE INDEX IF NOT EXISTS idx_users_supabase_uid ON users(supabase_uid);

