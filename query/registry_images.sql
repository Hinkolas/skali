-- name: ListRegistryImages :many
SELECT * FROM registry_images ORDER BY repository, tag;

-- name: GetRegistryImageByID :one
SELECT * FROM registry_images WHERE id = $1;

-- The catalog key lookup: does the mirror serve this (repository, tag), and
-- at which digest?
-- name: GetRegistryImageByRepoTag :one
SELECT * FROM registry_images WHERE repository = $1 AND tag = $2;

-- Re-importing a tag moves the digest pin in place; imported_at keeps the
-- first import's time.
-- name: UpsertRegistryImage :one
INSERT INTO registry_images (id, repository, tag, digest, size_bytes)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (repository, tag) DO UPDATE SET
    digest     = EXCLUDED.digest,
    size_bytes = EXCLUDED.size_bytes,
    updated_at = now()
RETURNING *;

-- name: DeleteRegistryImageByID :execrows
DELETE FROM registry_images WHERE id = $1;
