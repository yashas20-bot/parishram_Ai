-- name: CreateAuditLog :one
INSERT INTO audit_logs (
    user_id,
    action,
    entity_type,
    entity_id,
    details,
    ip_address
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: ListAuditLogsByUserID :many
SELECT * FROM audit_logs
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListAuditLogsByEntity :many
SELECT * FROM audit_logs
WHERE entity_type = $1 AND entity_id = $2
ORDER BY created_at DESC;
