-- +goose Up

-- What a deployment, rollback, or restart run changes, written by the
-- deploy service: the plan's service and value changes, the revision the
-- run moves the target to, the source of a promotion, and the
-- applications it restarts. Explanatory like the rest of the journal, so
-- history views can say what each rollout did without recomputing plans;
-- it goes with the run when retention prunes it. NULL on every other kind
-- of run and on runs from before the column existed.
ALTER TABLE runs ADD COLUMN deployment JSONB;

-- +goose Down
ALTER TABLE runs DROP COLUMN deployment;
