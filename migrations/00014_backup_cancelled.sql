-- +goose Up

-- A backup or restore row a cancel reached: before it started, so it never
-- runs, or while it ran, so its work was interrupted. Either way it is
-- final and never resumes.
ALTER TABLE backups DROP CONSTRAINT backups_status_check;
ALTER TABLE backups ADD CONSTRAINT backups_status_check
    CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled'));

-- +goose Down
UPDATE backups SET status = 'failed', error = COALESCE(error, 'cancelled')
WHERE status = 'cancelled';
ALTER TABLE backups DROP CONSTRAINT backups_status_check;
ALTER TABLE backups ADD CONSTRAINT backups_status_check
    CHECK (status IN ('pending', 'running', 'succeeded', 'failed'));
