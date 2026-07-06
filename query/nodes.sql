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

-- name: RecordNodeHeartbeat :exec
UPDATE nodes SET arch = $2, os = $3, skalid_version = $4,
    status = 'online', last_seen = now(), updated_at = now()
WHERE id = $1;

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
