-- Stamp one application's restart. The upsert keeps exactly one row per
-- application; the reconciler renders the stamp as the pod-template restart
-- annotation, so bumping it rolls that application's workload.
-- name: StampApplicationRestart :one
INSERT INTO environment_restarts (environment_id, application_key)
VALUES ($1, $2)
ON CONFLICT (environment_id, application_key)
DO UPDATE SET restarted_at = now()
RETURNING *;

-- name: ListEnvironmentRestarts :many
SELECT * FROM environment_restarts WHERE environment_id = $1;
