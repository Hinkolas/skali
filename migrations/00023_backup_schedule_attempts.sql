-- +goose Up

-- attempts counts the backups the scheduler started for the fire in
-- last_fire_at: the fire itself and its retries. A seeded schedule has
-- started none. A failed scheduled backup is retried a bounded number of
-- times before the schedule's next fire; the count is what bounds it.
ALTER TABLE backup_schedules ADD COLUMN attempts INT NOT NULL DEFAULT 0;
UPDATE backup_schedules SET attempts = 1 WHERE last_backup_id IS NOT NULL;

-- +goose Down
ALTER TABLE backup_schedules DROP COLUMN attempts;
