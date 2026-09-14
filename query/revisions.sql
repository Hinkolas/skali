-- Revisions are immutable: insert and read, never update. Re-preparing
-- identical inputs reuses the row via the (environment, checksum) unique.

-- name: InsertRevision :execrows
INSERT INTO revisions
    (id, project_id, environment_id, definition_version_id, schema_version,
     checksum, definition_hash, values_hash, compiler_version, document)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (environment_id, checksum) DO NOTHING;

-- name: GetRevisionByID :one
SELECT * FROM revisions WHERE id = $1;

-- name: GetRevisionByChecksum :one
SELECT * FROM revisions WHERE environment_id = $1 AND checksum = $2;

-- name: ListRevisions :many
SELECT id, project_id, environment_id, definition_version_id, schema_version,
       checksum, definition_hash, values_hash, compiler_version, created_at
FROM revisions
WHERE environment_id = $1
ORDER BY created_at DESC;
