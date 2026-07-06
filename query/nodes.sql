-- name: CreateNode :one
INSERT INTO nodes (id, name, roles, advertise_addr, arch, os, skalid_version, cert_serial)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY created_at;

-- name: GetNodeByID :one
SELECT * FROM nodes WHERE id = $1;

-- name: GetMasterNode :one
SELECT * FROM nodes WHERE 'master' = ANY(roles) LIMIT 1;

-- name: SetNodeName :one
UPDATE nodes SET name = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- name: SetNodeRoles :one
UPDATE nodes SET roles = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- name: SetNodePublicAddr :one
UPDATE nodes SET public_addr = $2, updated_at = now() WHERE id = $1
RETURNING *;

-- Metrics params are nullable: a node whose sampler isn't warm yet heartbeats
-- without a snapshot, and the previous latest values are left untouched.
-- name: RecordNodeHeartbeat :exec
UPDATE nodes SET arch = $2, os = $3, skalid_version = $4,
    cpu_pct         = COALESCE(sqlc.narg(cpu_pct), cpu_pct),
    mem_used        = COALESCE(sqlc.narg(mem_used), mem_used),
    mem_total       = COALESCE(sqlc.narg(mem_total), mem_total),
    disk_used       = COALESCE(sqlc.narg(disk_used), disk_used),
    disk_total      = COALESCE(sqlc.narg(disk_total), disk_total),
    net_rx_rate     = COALESCE(sqlc.narg(net_rx_rate), net_rx_rate),
    net_tx_rate     = COALESCE(sqlc.narg(net_tx_rate), net_tx_rate),
    disk_read_rate  = COALESCE(sqlc.narg(disk_read_rate), disk_read_rate),
    disk_write_rate = COALESCE(sqlc.narg(disk_write_rate), disk_write_rate),
    load1           = COALESCE(sqlc.narg(load1), load1),
    status = 'online', last_seen = now(), updated_at = now()
WHERE id = $1;

-- name: InsertNodeMetrics :exec
INSERT INTO node_metrics (node_id, cpu_pct, mem_used, mem_total, disk_used,
    disk_total, net_rx_rate, net_tx_rate, disk_read_rate, disk_write_rate, load1)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- Bucketed 24h history for the UI: ≤720 points instead of ~5760 raw rows.
-- 2-minute buckets keep freshly-enrolled nodes from staring at an empty chart
-- for a quarter hour; the client decimates further to ~180 render points.
-- Buckets with no samples are simply absent (gap semantics preserved).
-- name: ListNodeMetricsBucketed :many
SELECT
    date_bin('2 minutes', sampled_at, 'epoch'::timestamptz)::timestamptz AS bucket,
    avg(cpu_pct)::real            AS cpu_pct,
    avg(mem_used)::bigint         AS mem_used,
    avg(mem_total)::bigint        AS mem_total,
    avg(disk_used)::bigint        AS disk_used,
    avg(disk_total)::bigint       AS disk_total,
    avg(net_rx_rate)::bigint      AS net_rx_rate,
    avg(net_tx_rate)::bigint      AS net_tx_rate,
    avg(disk_read_rate)::bigint   AS disk_read_rate,
    avg(disk_write_rate)::bigint  AS disk_write_rate,
    avg(load1)::real              AS load1
FROM node_metrics
WHERE node_id = $1 AND sampled_at > now() - interval '24 hours'
GROUP BY bucket
ORDER BY bucket;

-- name: PruneNodeMetrics :execrows
DELETE FROM node_metrics WHERE sampled_at < now() - interval '24 hours';

-- The poller's staleness sweep: only rows it has stopped reaching flip, so a
-- single failed tick never flaps a node offline.
-- name: MarkStaleNodesOffline :execrows
UPDATE nodes SET status = 'offline', updated_at = now()
WHERE status = 'online' AND (last_seen IS NULL OR last_seen < $1);

-- name: DeleteNodeByID :execrows
DELETE FROM nodes WHERE id = $1;

-- name: CreateJoinToken :one
INSERT INTO join_tokens (id, hash, roles, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetJoinTokenByID :one
SELECT * FROM join_tokens WHERE id = $1;

-- Atomic burn: rowcount 0 means already used or expired — enrollment aborts.
-- name: BurnJoinToken :execrows
UPDATE join_tokens SET used_at = now()
WHERE id = $1 AND used_at IS NULL AND expires_at > now();

-- Burned tokens are kept a day for operator visibility, then swept with the
-- expired ones.
-- name: SweepExpiredJoinTokens :execrows
DELETE FROM join_tokens
WHERE expires_at < now() OR used_at < now() - interval '1 day';

-- name: GetClusterCA :one
SELECT * FROM cluster_ca WHERE id;

-- Boot race: two concurrent first boots both try to create the CA; the loser
-- gets no row back and re-reads the winner's.
-- name: CreateClusterCA :one
INSERT INTO cluster_ca (cert_pem, key_cipher)
VALUES ($1, $2)
ON CONFLICT (id) DO NOTHING
RETURNING *;
