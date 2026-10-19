-- +goose Up

-- The service a step's work belongs to, by its dotted name
-- (applications.web, databases.main, buckets.assets), recorded by the
-- writer that creates the step, so a client can show a run's progress per
-- service without parsing step keys. NULL marks environment-wide work
-- (validate, render, prune, verify, activate) and every step journaled
-- before the column existed.
ALTER TABLE steps ADD COLUMN service TEXT;

-- +goose Down
ALTER TABLE steps DROP COLUMN service;
