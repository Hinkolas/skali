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

-- The cluster-wide admin list; node_id narrows to one node when present.
-- name: ListVolumes :many
SELECT sqlc.embed(node_volumes), nodes.name AS node_name
FROM node_volumes
JOIN nodes ON nodes.id = node_volumes.node_id
WHERE sqlc.narg(node_id)::uuid IS NULL OR node_volumes.node_id = sqlc.narg(node_id)
ORDER BY nodes.name, node_volumes.name;
