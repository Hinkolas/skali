-- name: CreateDatabaseCluster :one
INSERT INTO database_clusters (
    id, name, engine, major, class, environment_id, claim_id,
    instances, storage_bytes, image, state
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetDatabaseCluster :one
SELECT * FROM database_clusters WHERE id = $1;

-- name: GetDatabaseClusterForUpdate :one
SELECT * FROM database_clusters WHERE id = $1 FOR UPDATE;

-- name: GetLiveDatabaseClusterByName :one
SELECT * FROM database_clusters WHERE name = $1 AND state <> 'released';

-- Packing lookups, one per pool class. Live means not released: a releasing
-- pool still occupies its packing slot so a racing claim cannot recreate the
-- same name while teardown is in flight.
-- name: GetLiveSharedDatabaseCluster :one
SELECT * FROM database_clusters
WHERE engine = $1 AND major = $2 AND class = 'shared' AND state <> 'released';

-- name: GetLiveEnvironmentDatabaseCluster :one
SELECT * FROM database_clusters
WHERE engine = $1 AND major = $2 AND environment_id = $3
  AND class = 'environment' AND state <> 'released';

-- name: GetLiveDedicatedDatabaseCluster :one
SELECT * FROM database_clusters
WHERE claim_id = $1 AND class = 'dedicated' AND state <> 'released';

-- name: ListLiveDatabaseClusters :many
SELECT * FROM database_clusters WHERE state <> 'released' ORDER BY name;

-- Compare-and-swap state change; the caller verifies the transition against
-- the cluster state machine before issuing it, the WHERE guard makes a lost
-- race a 0-row no-op instead of an illegal jump.
-- name: SetDatabaseClusterState :execrows
UPDATE database_clusters
SET state = sqlc.arg(to_state), updated_at = now()
WHERE id = $1 AND state = sqlc.arg(from_state);

-- Desired-shape changes (tier growth, storage growth). Never changes class,
-- engine, or major: those are a different pool by definition.
-- name: SetDatabaseClusterShape :execrows
UPDATE database_clusters
SET instances = $2, storage_bytes = $3, updated_at = now()
WHERE id = $1;
