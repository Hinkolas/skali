-- name: CreateBackupCode :exec
INSERT INTO backup_codes (id, two_factor_id, code_hash)
VALUES ($1, $2, $3);

-- name: ListUnusedBackupCodes :many
SELECT backup_codes.* FROM backup_codes
JOIN two_factors ON two_factors.id = backup_codes.two_factor_id
WHERE two_factors.user_id = $1 AND backup_codes.used_at IS NULL;

-- name: ConsumeBackupCode :execrows
UPDATE backup_codes SET used_at = now() WHERE id = $1 AND used_at IS NULL;

-- name: DeleteBackupCodesByTwoFactorID :exec
DELETE FROM backup_codes WHERE two_factor_id = $1;
