-- name: CreateEnvironmentTarget :exec
INSERT INTO environment_targets (environment_id)
VALUES ($1);

-- name: GetEnvironmentTarget :one
SELECT * FROM environment_targets WHERE environment_id = $1;

-- The single writer of the target pointer; runs only inside the deploy
-- promotion or rollback transaction. Promoting resurrects an environment
-- that was taken down, but never one that is releasing: purge is one-way,
-- so a promote racing a purge fails on the 0-row result.
-- name: SetEnvironmentTarget :execrows
UPDATE environment_targets
SET target_revision_id = $2, state = 'active', updated_at = now()
WHERE environment_id = $1 AND state <> 'releasing';

-- The single writer of the active pointer; only reconciliation calls it, and
-- only after the revision's required health conditions pass. The target guard
-- makes a late activation of a superseded revision a no-op (0 rows).
-- name: SetEnvironmentActiveRevision :execrows
UPDATE environment_targets
SET active_revision_id = $2, updated_at = now()
WHERE environment_id = $1 AND target_revision_id = $2;

-- name: ListEnvironmentTargets :many
SELECT * FROM environment_targets;

-- Environments whose cluster state has not yet reached the promoted target;
-- the reconcile boot audit and periodic audit enqueue these.
-- name: ListEnvironmentsOutOfSync :many
SELECT * FROM environment_targets
WHERE target_revision_id IS DISTINCT FROM active_revision_id;

-- The persisted destructive decisions behind skali dev down. Down removes
-- the runtime workloads but keeps data; both pointers are cleared so the
-- next deployment is never "nothing to deploy" and re-promotes into the
-- surviving namespace. Releasing is one-way: once set, nothing revives the
-- environment and reconciliation ends by deleting the row itself.
-- name: MarkEnvironmentDown :execrows
UPDATE environment_targets
SET state = 'down', target_revision_id = NULL, active_revision_id = NULL,
    updated_at = now()
WHERE environment_id = $1 AND state <> 'releasing';

-- name: MarkEnvironmentReleasing :execrows
UPDATE environment_targets
SET state = 'releasing', target_revision_id = NULL, active_revision_id = NULL,
    updated_at = now()
WHERE environment_id = $1;

-- Automatic fallback and cancellation: return the target to the last active
-- revision. The compare-and-swap on the expected target means a newer
-- deployment's promotion is never clobbered, and without an active revision
-- (first deployment) there is nothing to fall back to (0 rows = no-op).
-- name: FallbackEnvironmentTarget :execrows
UPDATE environment_targets
SET target_revision_id = active_revision_id, updated_at = now()
WHERE environment_id = $1 AND target_revision_id = $2
  AND active_revision_id IS NOT NULL
  AND target_revision_id IS DISTINCT FROM active_revision_id;
