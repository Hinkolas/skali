-- name: CreateDatabasePlacement :one
INSERT INTO database_placements (id, claim_id, cluster_id)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetActiveDatabasePlacement :one
SELECT * FROM database_placements
WHERE claim_id = $1 AND superseded_at IS NULL;

-- Relocation and unbinding supersede rather than delete, so placement
-- history explains where a tenant has lived.
-- name: SupersedeDatabasePlacement :execrows
UPDATE database_placements
SET superseded_at = now()
WHERE claim_id = $1 AND superseded_at IS NULL;

-- name: ListActiveDatabasePlacementsByCluster :many
SELECT * FROM database_placements
WHERE cluster_id = $1 AND superseded_at IS NULL
ORDER BY created_at;
