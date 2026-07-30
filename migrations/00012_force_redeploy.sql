-- +goose Up

-- Force redeployments (skali deploy --force): a persisted restart stamp the
-- reconciler renders as a pod-template annotation, rolling every application
-- workload even when the promoted revision is unchanged (kubectl rollout
-- restart semantics; persisted on the target row so level-based convergence
-- stays deterministic across passes and daemon restarts). Stateful services
-- never read it: force recreates pods, never data.
ALTER TABLE environment_targets ADD COLUMN restarted_at TIMESTAMPTZ;

-- The force decision travels on the deployment row so completion, not the
-- client, stamps the restart inside the promotion transaction.
ALTER TABLE deployments ADD COLUMN restart BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down

ALTER TABLE deployments DROP COLUMN restart;
ALTER TABLE environment_targets DROP COLUMN restarted_at;
