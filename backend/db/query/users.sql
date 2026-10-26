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

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1 LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1 LIMIT 1;

-- name: UpdateUserSpendingLimit :one
UPDATE users
SET spending_limit = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: ListUsers :many
SELECT * FROM users
ORDER BY id ASC;
