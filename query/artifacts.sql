-- name: CreateArtifact :one
INSERT INTO artifacts (id, project_id, application, kind, upstream, context_hash)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetArtifactByID :one
SELECT * FROM artifacts WHERE id = $1;

-- Row lock so phase transitions are guarded under the lifecycle machine.
-- name: GetArtifactForUpdate :one
SELECT * FROM artifacts WHERE id = $1 FOR UPDATE;

-- name: SetArtifactVerified :exec
UPDATE artifacts
SET phase = 'verified', reference = $2, digest = $3, provenance = $4,
    verified_at = now(), updated_at = now()
WHERE id = $1;

-- name: SetArtifactPhase :exec
UPDATE artifacts SET phase = $2, updated_at = now() WHERE id = $1;

-- name: CountArtifactLeases :one
SELECT count(*) FROM artifact_leases WHERE artifact_id = $1;

-- name: SweepPendingArtifacts :execrows
UPDATE artifacts SET phase = 'abandoned', updated_at = now()
WHERE phase = 'pending' AND created_at < $1;
