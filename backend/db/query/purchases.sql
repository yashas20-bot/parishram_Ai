-- name: CreatePurchaseProposal :one
INSERT INTO purchases (
    user_id,
    merchant_name,
    item_name,
    amount,
    currency,
    status,
    risk_score,
    risk_factors,
    notes
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
RETURNING *;

-- name: GetPurchaseByID :one
SELECT * FROM purchases
WHERE id = $1 LIMIT 1;

-- name: GetPurchaseByIDAndUser :one
SELECT * FROM purchases
WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: UpdatePurchaseStatus :one
UPDATE purchases
SET status = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: ListPurchasesByUserID :many
SELECT * FROM purchases
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
