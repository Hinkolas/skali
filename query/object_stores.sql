-- name: CreateObjectStore :one
INSERT INTO object_stores (
    id, name, masters, volume_servers, replication,
    volume_storage_bytes, image, state
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetObjectStore :one
SELECT * FROM object_stores WHERE id = $1;

-- name: GetObjectStoreForUpdate :one
SELECT * FROM object_stores WHERE id = $1 FOR UPDATE;

-- Live means not released: a releasing store still occupies the name so a
-- racing claim cannot recreate it while teardown is in flight.
-- name: GetLiveObjectStore :one
SELECT * FROM object_stores WHERE state <> 'released';

-- Compare-and-swap state change; the caller verifies the transition against
-- the store state machine before issuing it, the WHERE guard makes a lost
-- race a 0-row no-op instead of an illegal jump.
-- name: SetObjectStoreState :execrows
UPDATE object_stores
SET state = sqlc.arg(to_state), updated_at = now()
WHERE id = $1 AND state = sqlc.arg(from_state);

-- Desired-shape changes (topology growth). Never changes the name or image:
-- those identify the store.
-- name: SetObjectStoreShape :execrows
UPDATE object_stores
SET masters = $2, volume_servers = $3, replication = $4,
    volume_storage_bytes = $5, updated_at = now()
WHERE id = $1;
