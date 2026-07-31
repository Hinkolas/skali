-- +goose Up

-- The dev quiescing states are retired (owner decision 2026-07-31): the dev
-- substrate stays up while the platform runs, so pools no longer hibernate
-- and the object store no longer stops when unused. Surviving rows fold
-- back to active before the shrunken lifecycle machines ever see them; the
-- substrate's next converge re-applies the workloads (hibernation off,
-- real replica counts) with all data intact. Rows in these states can only
-- exist on local dev installations.
UPDATE database_clusters SET state = 'active', updated_at = now() WHERE state = 'hibernated';
UPDATE object_stores SET state = 'active', updated_at = now() WHERE state = 'stopped';

ALTER TABLE database_clusters DROP CONSTRAINT database_clusters_state_check;
ALTER TABLE database_clusters ADD CONSTRAINT database_clusters_state_check
    CHECK (state IN ('active', 'releasing', 'released'));
ALTER TABLE object_stores DROP CONSTRAINT object_stores_state_check;
ALTER TABLE object_stores ADD CONSTRAINT object_stores_state_check
    CHECK (state IN ('active', 'releasing', 'released'));
