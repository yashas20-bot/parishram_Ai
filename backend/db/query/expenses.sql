-- name: CreateExpense :one
INSERT INTO expenses (
    user_id,
    category,
    amount,
    currency,
    description,
    spent_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetExpenseByIDAndUser :one
-- User-facing scoped read query (prevents IDOR)
SELECT * FROM expenses
WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: ListExpensesByUserID :many
SELECT * FROM expenses
WHERE user_id = $1
ORDER BY spent_at DESC
LIMIT $2 OFFSET $3;

-- name: GetTotalExpensesInPeriod :one
SELECT COALESCE(SUM(amount), 0)::BIGINT AS total_amount
FROM expenses
WHERE user_id = $1
  AND spent_at >= $2
  AND spent_at <= $3;

-- name: GetExpensesByCategory :many
SELECT category, COALESCE(SUM(amount), 0)::BIGINT AS total_amount, COUNT(*)::BIGINT AS count
FROM expenses
WHERE user_id = $1
  AND spent_at >= $2
  AND spent_at <= $3
GROUP BY category
ORDER BY total_amount DESC;

-- name: DeleteExpenseForUser :exec
-- User-facing scoped deletion (prevents IDOR)
DELETE FROM expenses
WHERE id = $1 AND user_id = $2;

-- name: GetMonthlyExpenseTotalForUser :one
-- Calculates the authenticated user's confirmed expense total for a monthly period.
-- Uses start-inclusive, end-exclusive interval [start, end) and filters by currency to prevent mixing currencies.
SELECT COALESCE(SUM(amount), 0)::BIGINT AS total_amount
FROM expenses
WHERE user_id = $1
  AND currency = $2
  AND spent_at >= $3
  AND spent_at < $4;

-- name: GetMonthlyExpensesByCategory :many
SELECT category, COALESCE(SUM(amount), 0)::BIGINT AS total_amount, COUNT(*)::BIGINT AS count
FROM expenses
WHERE user_id = $1
  AND currency = $2
  AND spent_at >= $3
  AND spent_at < $4
GROUP BY category
ORDER BY total_amount DESC;

