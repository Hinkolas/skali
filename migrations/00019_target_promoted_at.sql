-- +goose Up

-- When the target pointer was last promoted: written only by the writes
-- that start a new rollout of a target (promotion, rollback, a completed
-- restore's resume), never by activation, restarts, or fallback, which all
-- touch updated_at. Release Jobs carry it as a label, so a failed release
-- belongs to exactly one promotion and a later pass never mistakes it for
-- a leftover of an earlier one. Existing rows start from updated_at, the
-- closest record of their last promotion.
ALTER TABLE environment_targets ADD COLUMN promoted_at TIMESTAMPTZ NOT NULL DEFAULT now();
UPDATE environment_targets SET promoted_at = updated_at;

-- +goose Down
ALTER TABLE environment_targets DROP COLUMN promoted_at;
