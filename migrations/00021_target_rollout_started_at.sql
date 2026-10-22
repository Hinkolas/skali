-- +goose Up

-- When the target's current rollout began: written by every write that
-- starts one (promotion, rollback, a completed restore's resume, a restart
-- stamp), never by activation, fallback, or teardown, which all touch
-- updated_at. The rollout deadline counts from it whether or not a run
-- explains the rollout. Existing rows start from updated_at, the clock
-- the deadline used before.
ALTER TABLE environment_targets ADD COLUMN rollout_started_at TIMESTAMPTZ NOT NULL DEFAULT now();
UPDATE environment_targets SET rollout_started_at = updated_at;

-- +goose Down
ALTER TABLE environment_targets DROP COLUMN rollout_started_at;
