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
