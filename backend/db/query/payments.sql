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
-- Internal/system query
SELECT * FROM payments
WHERE id = $1 LIMIT 1;

-- name: GetPaymentByIDAndUser :one
-- User-facing scoped read query (prevents IDOR)
SELECT * FROM payments
WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: GetPaymentByPayPalOrderID :one
-- Trusted query for PayPal webhook processing and status polling
SELECT * FROM payments
WHERE paypal_order_id = $1 LIMIT 1;

-- name: UpdatePaymentStatus :one
-- Internal/system query
UPDATE payments
SET status = $2,
    capture_id = COALESCE($3, capture_id),
    failure_reason = COALESCE($4, failure_reason),
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: UpdatePaymentStatusForUser :one
-- User-facing payment status update (prevents IDOR: requires authenticated user_id)
UPDATE payments
SET status = $2,
    capture_id = COALESCE($3, capture_id),
    failure_reason = COALESCE($4, failure_reason),
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND user_id = $5
RETURNING *;

-- name: UpdatePaymentStatusByPayPalOrderID :one
-- Trusted query for idempotent PayPal webhook updates
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

-- name: GetPaymentByPurchaseIDAndUser :one
-- Retrieves the most recent payment record associated with a purchase for the authenticated user
SELECT * FROM payments
WHERE purchase_id = $1 AND user_id = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: ListPaymentsByPurchaseID :many
-- Lists all payment records associated with a purchase for the authenticated user
SELECT * FROM payments
WHERE purchase_id = $1 AND user_id = $2
ORDER BY created_at DESC;
