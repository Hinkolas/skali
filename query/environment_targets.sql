-- name: CreateEnvironmentTarget :exec
INSERT INTO environment_targets (environment_id)
VALUES ($1);

-- name: GetEnvironmentTarget :one
SELECT * FROM environment_targets WHERE environment_id = $1;

-- GetEnvironmentPass reads what a reconcile pass starts from in one round
-- trip: the environment, its target pointers, and its running run with the
-- revision it was handed off for, which are NULL when nothing runs. intercepts maps each intercepted application to its host
-- ports and restarts each restarted application to its stamp; both are
-- NULL when empty. claims_hostnames reports whether the environment holds
-- any hostname claim.
-- name: GetEnvironmentPass :one
SELECT sqlc.embed(environments), sqlc.embed(environment_targets),
       runs.id AS run_id, runs.kind AS run_kind, runs.rollout_revision_id AS run_rollout_revision_id,
       (SELECT jsonb_object_agg(environment_intercepts.application_key, environment_intercepts.ports)
        FROM environment_intercepts
        WHERE environment_intercepts.environment_id = environments.id)::jsonb AS intercepts,
       (SELECT jsonb_object_agg(environment_restarts.application_key, environment_restarts.restarted_at)
        FROM environment_restarts
        WHERE environment_restarts.environment_id = environments.id)::jsonb AS restarts,
       EXISTS (SELECT 1 FROM hostname_claims
               WHERE hostname_claims.environment_id = environments.id) AS claims_hostnames
FROM environments
JOIN environment_targets ON environment_targets.environment_id = environments.id
LEFT JOIN runs ON runs.environment_id = environments.id AND runs.status = 'running'
WHERE environments.id = $1;

-- The single writer of the target pointer; runs only inside the deploy
-- promotion or rollback transaction. Promoting resurrects an environment
-- that was taken down, but never one that is releasing: purge is one-way,
-- so a promote racing a purge fails on the 0-row result. Nor one an
-- incomplete restore holds: its data may be partial, and only a restore
-- that completes (ResumeRestoredEnvironment) or an explicit acceptance
-- (ClearIncompleteRestore) lifts the hold. With restart set, the same
-- write stamps a workload restart (a forced deployment). Every call is a
-- new promotion, a forced redeploy of the same revision included, so it
-- stamps promoted_at, which binds the promotion's release Jobs.
-- name: SetEnvironmentTarget :execrows
UPDATE environment_targets
SET target_revision_id = sqlc.narg(target_revision_id), state = 'active', updated_at = now(),
    promoted_at = now(),
    restarted_at = CASE WHEN sqlc.arg(restart)::boolean THEN now() ELSE restarted_at END
WHERE environment_id = sqlc.arg(environment_id) AND state <> 'releasing'
  AND incomplete_restore_id IS NULL;

-- The single writer of the active pointer; only reconciliation calls it, and
-- only after the revision's required health conditions pass. The target guard
-- makes a late activation of a superseded revision a no-op (0 rows).
-- name: SetEnvironmentActiveRevision :execrows
UPDATE environment_targets
SET active_revision_id = $2, updated_at = now()
WHERE environment_id = $1 AND target_revision_id = $2;

-- Force redeployments stamp a restart inside the promotion transaction; the
-- reconciler renders the stamp as a pod-template annotation so every
-- application workload rolls even when the revision is unchanged. Stateful
-- services never read it.
-- name: StampEnvironmentRestart :execrows
UPDATE environment_targets
SET restarted_at = now(), updated_at = now()
WHERE environment_id = $1 AND state <> 'releasing';

-- A service restart begins a rollout without moving the target; touching
-- updated_at restarts the rollout-deadline clock for the adopted restart
-- run, exactly like a promotion does through SetEnvironmentTarget.
-- name: TouchEnvironmentTarget :execrows
UPDATE environment_targets
SET updated_at = now()
WHERE environment_id = $1 AND state <> 'releasing';

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

-- A restore's stop: MarkEnvironmentDown plus the restore hold, in one
-- statement, so the environment is never down for a restore without the
-- hold that keeps deployments from resuming it on partial data. A restore
-- re-run over an earlier one's hold takes the hold over.
-- name: MarkEnvironmentRestoring :execrows
UPDATE environment_targets
SET state = 'down', target_revision_id = NULL, active_revision_id = NULL,
    incomplete_restore_id = sqlc.arg(restore_id)::uuid,
    incomplete_restore_components = COALESCE(sqlc.arg(components)::text[], '{}'),
    updated_at = now()
WHERE environment_id = sqlc.arg(environment_id) AND state <> 'releasing';

-- A completed restore's resume: the target moves back and the hold lifts in
-- the same write. Only the restore holding the environment resumes it, and
-- only while its run still runs: a teardown cancels the run before it takes
-- the environment down, and a restore finishing its last component must
-- not undo that. Resuming promotes the target again, so the restored
-- revision's release runs as a new promotion.
-- name: ResumeRestoredEnvironment :execrows
UPDATE environment_targets
SET target_revision_id = sqlc.arg(target_revision_id)::uuid, state = 'active',
    incomplete_restore_id = NULL, incomplete_restore_components = '{}',
    updated_at = now(), promoted_at = now()
WHERE environment_targets.environment_id = sqlc.arg(environment_id)
  AND environment_targets.state <> 'releasing'
  AND environment_targets.incomplete_restore_id = sqlc.arg(restore_id)::uuid
  AND EXISTS (SELECT 1 FROM backups JOIN runs ON runs.id = backups.run_id
              WHERE backups.id = sqlc.arg(restore_id)::uuid AND runs.status = 'running');

-- Accepting the data an incomplete restore left: the hold lifts and the
-- environment stays down until the next deployment. The restore id is the
-- one the caller was shown, so a hold a newer restore took over stays.
-- name: ClearIncompleteRestore :execrows
UPDATE environment_targets
SET incomplete_restore_id = NULL, incomplete_restore_components = '{}'
WHERE environment_id = sqlc.arg(environment_id)
  AND incomplete_restore_id = sqlc.arg(restore_id)::uuid;

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
