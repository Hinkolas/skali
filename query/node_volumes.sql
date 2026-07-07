-- name: UpsertNodeVolume :exec
INSERT INTO node_volumes (node_id, name, driver, scope, mountpoint,
    labels, containers, volume_created)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (node_id, name) DO UPDATE SET
    driver         = EXCLUDED.driver,
    scope          = EXCLUDED.scope,
    mountpoint     = EXCLUDED.mountpoint,
    labels         = EXCLUDED.labels,
    containers     = EXCLUDED.containers,
    volume_created = EXCLUDED.volume_created,
    last_seen      = now();

-- A present report is authoritative for its node: volumes it did not mention
-- are deleted (inventory mirrors reality, no gone breadcrumbs). Callers must
-- skip this entirely for absent reports (unknown ≠ empty) or an agent
-- restart would wipe the node's inventory.
-- name: DeleteMissingNodeVolumes :execrows
DELETE FROM node_volumes
WHERE node_id = $1 AND NOT (name = ANY(sqlc.arg(names)::text[]));

-- name: ListNodeVolumes :many
SELECT * FROM node_volumes WHERE node_id = $1 ORDER BY name;
