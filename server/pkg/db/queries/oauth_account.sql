-- name: GetOAuthAccount :one
SELECT * FROM oauth_account
WHERE provider = $1 AND provider_user_id = $2;

-- name: CreateOAuthAccount :one
INSERT INTO oauth_account (user_id, provider, provider_user_id, email)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListOAuthAccountsByUser :many
SELECT * FROM oauth_account
WHERE user_id = $1
ORDER BY created_at ASC;

-- name: DeleteOAuthAccount :exec
DELETE FROM oauth_account
WHERE user_id = $1 AND provider = $2;

-- name: GetOAuthAccountByUserAndProvider :one
SELECT * FROM oauth_account
WHERE user_id = $1 AND provider = $2;
