-- Opens the pool's incident, or returns the one already open: the partial
-- unique index admits one open row per pool, so a monitor that restarted
-- mid-incident keeps the original identity and start.
-- name: OpenPoolConnectionIncident :one
WITH inserted AS (
    INSERT INTO pool_connection_incidents (id, cluster_id, code, message, opened_at)
    VALUES ($1, $2, $3, $4, $5)
    ON CONFLICT (cluster_id) WHERE closed_at IS NULL DO NOTHING
    RETURNING id, cluster_id, code, message, opened_at, closed_at
)
SELECT id, cluster_id, code, message, opened_at, closed_at FROM inserted
UNION ALL
SELECT id, cluster_id, code, message, opened_at, closed_at FROM pool_connection_incidents
WHERE cluster_id = $2 AND closed_at IS NULL
LIMIT 1;

-- name: ClosePoolConnectionIncident :execrows
UPDATE pool_connection_incidents
SET closed_at = sqlc.arg(closed_at)
WHERE cluster_id = sqlc.arg(cluster_id) AND closed_at IS NULL;

-- name: ListOpenPoolConnectionIncidents :many
SELECT * FROM pool_connection_incidents WHERE closed_at IS NULL ORDER BY opened_at;

-- name: ListPoolConnectionIncidents :many
SELECT * FROM pool_connection_incidents
WHERE cluster_id = $1
ORDER BY opened_at DESC
LIMIT $2;
