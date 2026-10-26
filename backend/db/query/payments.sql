-- name: CreatePayment :one
INSERT INTO payments (
    user_id,
    purchase_id,
    paypal_order_id,
    amount,
    currency,
    status
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetPaymentByID :one
SELECT * FROM payments
WHERE id = $1 LIMIT 1;

-- name: GetPaymentByPayPalOrderID :one
SELECT * FROM payments
WHERE paypal_order_id = $1 LIMIT 1;

-- name: UpdatePaymentStatus :one
UPDATE payments
SET status = $2,
    capture_id = COALESCE($3, capture_id),
    failure_reason = COALESCE($4, failure_reason),
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: UpdatePaymentStatusByPayPalOrderID :one
UPDATE payments
SET status = $2,
    capture_id = COALESCE($3, capture_id),
    failure_reason = COALESCE($4, failure_reason),
    updated_at = CURRENT_TIMESTAMP
WHERE paypal_order_id = $1
RETURNING *;

-- name: ListPaymentsByUserID :many
SELECT * FROM payments
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
