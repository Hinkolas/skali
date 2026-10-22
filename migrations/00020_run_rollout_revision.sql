-- +goose Up

-- The revision whose rollout a deployment, rollback, or restart run hands
-- to the kernel, written in the transaction that moves the target or
-- stamps the restart. It is the handoff: before it is set the run still
-- belongs to its opener (a deployment preparing its revision), and a
-- reconcile pass must neither journal into it nor conclude it from a
-- target that predates it. The kernel adopts a run only while it equals
-- the target revision. NULL on every other kind of run.
ALTER TABLE runs ADD COLUMN rollout_revision_id UUID;

-- Runs already handed off when the column arrives keep being adopted: a
-- promoted deployment, or a rollout run whose rollout step exists (the
-- opener's last write before the handoff, or an earlier adoption's).
UPDATE runs SET rollout_revision_id = environment_targets.target_revision_id
FROM environment_targets
WHERE runs.status = 'running'
  AND runs.kind IN ('deployment', 'rollback', 'restart')
  AND environment_targets.environment_id = runs.environment_id
  AND (EXISTS (SELECT 1 FROM steps WHERE steps.run_id = runs.id AND steps.key = 'rollout')
       OR EXISTS (SELECT 1 FROM deployments
                  WHERE deployments.run_id = runs.id AND deployments.status = 'promoted'));

-- +goose Down
ALTER TABLE runs DROP COLUMN rollout_revision_id;
