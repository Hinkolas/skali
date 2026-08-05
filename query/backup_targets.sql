-- name: UpsertBackupTarget :one
INSERT INTO backup_targets (id, name, endpoint, region, bucket, prefix,
                            access_key_id, secret_access_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (name) DO UPDATE
SET endpoint          = EXCLUDED.endpoint,
    region            = EXCLUDED.region,
    bucket            = EXCLUDED.bucket,
    prefix            = EXCLUDED.prefix,
    access_key_id     = EXCLUDED.access_key_id,
    secret_access_key = EXCLUDED.secret_access_key,
    updated_at        = now()
RETURNING *;

-- name: GetBackupTargetByName :one
SELECT * FROM backup_targets WHERE name = $1;

-- name: ListBackupTargets :many
SELECT * FROM backup_targets ORDER BY name;

-- name: DeleteBackupTarget :execrows
DELETE FROM backup_targets WHERE name = $1;
