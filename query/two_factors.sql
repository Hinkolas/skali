-- name: UpsertPendingTwoFactor :one
INSERT INTO two_factors (id, user_id, secret)
VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO UPDATE
SET secret = EXCLUDED.secret,
    confirmed_at = NULL,
    last_used_step = 0,
    updated_at = now()
RETURNING *;

-- name: GetTwoFactorByUserID :one
SELECT * FROM two_factors WHERE user_id = $1;

-- name: ConfirmTwoFactor :execrows
UPDATE two_factors SET confirmed_at = now(), updated_at = now()
WHERE user_id = $1 AND confirmed_at IS NULL;

-- name: AdvanceTwoFactorStep :execrows
UPDATE two_factors SET last_used_step = $2, updated_at = now()
WHERE user_id = $1 AND last_used_step < $2;

-- name: DeleteTwoFactorByUserID :execrows
DELETE FROM two_factors WHERE user_id = $1;
