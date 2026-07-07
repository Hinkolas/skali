-- Stats params are nullable: a container whose stats aren't derived yet (one
-- sample so far) is upserted without them, and previous values are kept.
-- name: UpsertNodeContainer :exec
INSERT INTO node_containers (node_id, container_id, name, image, kind, state,
    health, exit_code, restart_count, labels, container_created, container_started,
    cpu_pct, mem_used, mem_limit, net_rx_rate, net_tx_rate)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
    sqlc.narg(cpu_pct), sqlc.narg(mem_used), sqlc.narg(mem_limit),
    sqlc.narg(net_rx_rate), sqlc.narg(net_tx_rate))
ON CONFLICT (node_id, container_id) DO UPDATE SET
    name              = EXCLUDED.name,
    image             = EXCLUDED.image,
    kind              = EXCLUDED.kind,
    state             = EXCLUDED.state,
    health            = EXCLUDED.health,
    exit_code         = EXCLUDED.exit_code,
    restart_count     = EXCLUDED.restart_count,
    labels            = EXCLUDED.labels,
    container_created = EXCLUDED.container_created,
    container_started = EXCLUDED.container_started,
    cpu_pct           = COALESCE(EXCLUDED.cpu_pct, node_containers.cpu_pct),
    mem_used          = COALESCE(EXCLUDED.mem_used, node_containers.mem_used),
    mem_limit         = COALESCE(EXCLUDED.mem_limit, node_containers.mem_limit),
    net_rx_rate       = COALESCE(EXCLUDED.net_rx_rate, node_containers.net_rx_rate),
    net_tx_rate       = COALESCE(EXCLUDED.net_tx_rate, node_containers.net_tx_rate),
    last_seen         = now();

-- A present report is authoritative for its node: anything it did not
-- mention is gone. Callers must skip this entirely for absent reports
-- (unknown ≠ empty) or an agent restart would mass-flip containers to gone.
-- name: MarkMissingNodeContainersGone :execrows
UPDATE node_containers SET state = 'gone'
WHERE node_id = $1 AND state <> 'gone' AND NOT (container_id = ANY(sqlc.arg(container_ids)::text[]));

-- The cluster-wide admin list; node_id narrows to one node when present.
-- name: ListContainers :many
SELECT sqlc.embed(node_containers), nodes.name AS node_name
FROM node_containers
JOIN nodes ON nodes.id = node_containers.node_id
WHERE sqlc.narg(node_id)::uuid IS NULL OR node_containers.node_id = sqlc.narg(node_id)
ORDER BY nodes.name, node_containers.name;

-- name: GetNodeContainer :one
SELECT * FROM node_containers WHERE node_id = $1 AND container_id = $2;

-- name: DeleteNodeContainer :execrows
DELETE FROM node_containers WHERE node_id = $1 AND container_id = $2;

-- Gone rows are operator breadcrumbs, not history; swept by the poller.
-- name: PruneGoneNodeContainers :execrows
DELETE FROM node_containers WHERE state = 'gone' AND last_seen < now() - interval '1 hour';
