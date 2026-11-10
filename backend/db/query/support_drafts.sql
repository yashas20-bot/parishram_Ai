-- name: CreateSupportDraft :one
INSERT INTO support_drafts (
    user_id,
    payment_id,
    request_type,
    subject,
    draft_body,
    status
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetSupportDraftByID :one
-- Internal/system query
SELECT * FROM support_drafts
WHERE id = $1 LIMIT 1;

-- name: GetSupportDraftByIDAndUser :one
-- User-facing scoped read query (prevents IDOR)
SELECT * FROM support_drafts
WHERE id = $1 AND user_id = $2 LIMIT 1;

-- name: ListSupportDraftsByUserID :many
SELECT * FROM support_drafts
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: UpdateSupportDraft :one
-- Internal/system query
UPDATE support_drafts
SET subject = $2,
    draft_body = $3,
    status = $4,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
RETURNING *;

-- name: UpdateSupportDraftForUser :one
-- User-facing draft update (prevents IDOR: requires authenticated user_id)
UPDATE support_drafts
SET subject = $2,
    draft_body = $3,
    status = $4,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND user_id = $5
RETURNING *;
