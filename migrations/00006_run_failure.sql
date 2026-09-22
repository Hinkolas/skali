-- +goose Up

-- One-line reason for a run that ended failed, written by the code path
-- that knows why (the deploy service, the reconcile kernel, the backup
-- controller) rather than derived from step logs at read time, so failures
-- between steps are covered too. NULL on succeeded and cancelled runs and on
-- failed runs finished before this column existed. Step logs stay the
-- detail; this is the summary that lists and closing lines show.
ALTER TABLE runs ADD COLUMN failure TEXT;

-- +goose Down
ALTER TABLE runs DROP COLUMN failure;
