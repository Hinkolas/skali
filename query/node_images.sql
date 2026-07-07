-- name: UpsertNodeImage :exec
INSERT INTO node_images (node_id, image_id, repo_tags, repo_digests,
    size_bytes, dangling, containers, image_created)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (node_id, image_id) DO UPDATE SET
    repo_tags     = EXCLUDED.repo_tags,
    repo_digests  = EXCLUDED.repo_digests,
    size_bytes    = EXCLUDED.size_bytes,
    dangling      = EXCLUDED.dangling,
    containers    = EXCLUDED.containers,
    image_created = EXCLUDED.image_created,
    last_seen     = now();

-- A present report is authoritative for its node: images it did not mention
-- are deleted (inventory mirrors reality, no gone breadcrumbs). Callers must
-- skip this entirely for absent reports (unknown ≠ empty) or an agent
-- restart would wipe the node's inventory.
-- name: DeleteMissingNodeImages :execrows
DELETE FROM node_images
WHERE node_id = $1 AND NOT (image_id = ANY(sqlc.arg(image_ids)::text[]));

-- Largest first: the inventory exists to answer "what is eating disk".
-- name: ListNodeImages :many
SELECT * FROM node_images WHERE node_id = $1 ORDER BY size_bytes DESC, image_id;
