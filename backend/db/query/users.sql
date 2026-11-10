-- name: CreateUser :one
INSERT INTO users (
    email,
    name,
    spending_limit,
    currency
) VALUES (
    $1, $2, $3, $4
)
RETURNING *;

-- name: CreateUserWithSupabaseUID :one
INSERT INTO users (
    supabase_uid,
    email,
    name,
    spending_limit,
    currency
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;

-- name: UpsertUserWithSupabaseUID :one
-- Idempotent provisioning operation that handles repeated attempts using the unique constraint.
INSERT INTO users (
    supabase_uid,
    email,
    name,
    spending_limit,
    currency
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (supabase_uid) DO UPDATE
SET email = EXCLUDED.email,
    name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE users.name END,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1 LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1 LIMIT 1;

-- name: GetUserBySupabaseUID :one
SELECT * FROM users
WHERE supabase_uid = $1 LIMIT 1;

-- name: LinkSupabaseUIDByEmail :one
UPDATE users
SET supabase_uid = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE email = $1 AND (supabase_uid IS NULL OR supabase_uid = $2)
RETURNING *;

-- name: UpdateUserSpendingLimit :one
UPDATE users
SET spending_limit = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: UpdateUserSpendingLimitBySupabaseUID :one
UPDATE users
SET spending_limit = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE supabase_uid = $1
RETURNING *;

-- name: ListUsers :many
SELECT * FROM users
ORDER BY id ASC;
