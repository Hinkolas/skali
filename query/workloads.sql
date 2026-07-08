-- name: InsertWorkload :one
INSERT INTO workloads (id, name, kind, desired_state, replicas, constraints, image, spec)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListWorkloads :many
SELECT * FROM workloads ORDER BY name;

-- name: GetWorkloadByID :one
SELECT * FROM workloads WHERE id = $1;

-- User updates bump the generation so assignments can report convergence
-- lag. An image change passes NULL resolved_* (the reconciler re-resolves);
-- an unchanged image passes the current pin through.
-- name: UpdateWorkloadSpec :one
UPDATE workloads SET
    desired_state       = $2,
    replicas            = $3,
    constraints         = $4,
    image               = $5,
    spec                = $6,
    resolved_repository = $7,
    resolved_digest     = $8,
    generation          = generation + 1,
    last_error          = NULL,
    retries             = 0,
    next_attempt_at     = NULL,
    updated_at          = now()
WHERE id = $1
RETURNING *;

-- Reconciler output: the digest pin. Never bumps the generation — the pin is
-- how the spec is realized, not a spec change.
-- name: SetWorkloadResolvedImage :exec
UPDATE workloads SET
    resolved_repository = $2, resolved_digest = $3,
    last_error = NULL, retries = 0, next_attempt_at = NULL, updated_at = now()
WHERE id = $1;

-- name: SetWorkloadError :exec
UPDATE workloads SET
    last_error = $2, retries = $3, next_attempt_at = $4, updated_at = now()
WHERE id = $1;

-- Teardown is convergence too: DELETE marks, the reconciler removes the
-- containers, then hard-deletes the row.
-- name: MarkWorkloadDeleting :execrows
UPDATE workloads SET desired_state = 'deleting', updated_at = now() WHERE id = $1;

-- name: DeleteWorkload :execrows
DELETE FROM workloads WHERE id = $1;
