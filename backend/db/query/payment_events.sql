-- name: CreatePaymentEvent :one
INSERT INTO payment_events (
    payment_id,
    paypal_order_id,
    paypal_event_id,
    event_type,
    event_payload,
    status,
    processed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: CreatePaymentEventIfNotExist :one
INSERT INTO payment_events (
    payment_id,
    paypal_order_id,
    paypal_event_id,
    event_type,
    event_payload,
    status,
    processed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (paypal_event_id) DO NOTHING
RETURNING *;

-- name: GetPaymentEventByPayPalEventID :one
SELECT * FROM payment_events
WHERE paypal_event_id = $1 LIMIT 1;

-- name: ListPaymentEventsByOrderID :many
SELECT * FROM payment_events
WHERE paypal_order_id = $1
ORDER BY created_at ASC;

-- name: UpdatePaymentEventStatus :one
UPDATE payment_events
SET status = $2,
    processed_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;
