-- name: CreateOrUpdateBudget :one
INSERT INTO budgets (
    user_id,
    monthly_limit,
    alert_threshold_percent,
    currency
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (user_id) DO UPDATE
SET monthly_limit = EXCLUDED.monthly_limit,
    alert_threshold_percent = EXCLUDED.alert_threshold_percent,
    currency = EXCLUDED.currency,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetBudgetByUserID :one
SELECT * FROM budgets
WHERE user_id = $1 LIMIT 1;
