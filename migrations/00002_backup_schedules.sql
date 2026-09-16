-- +goose Up

-- Snapshots are marked by origin so retention only ever touches the ones a
-- manifest backup policy produced. policy names that policy (empty for
-- manual snapshots); strategy is how the snapshot was taken (complete is the
-- only strategy today). The S3 snapshot manifest carries the same three
-- fields, which is what a fresh installation reads.
ALTER TABLE backups
    ADD COLUMN trigger TEXT NOT NULL DEFAULT 'manual'
        CHECK (trigger IN ('manual', 'scheduled')),
    ADD COLUMN policy TEXT NOT NULL DEFAULT '',
    ADD COLUMN strategy TEXT NOT NULL DEFAULT 'complete'
        CHECK (strategy IN ('complete'));

-- Scheduler state per (environment, policy). last_fire_at is the cron fire
-- the scheduler last acted on, or the moment it first saw the policy: the
-- next due fire is the first cron time after it, so a newly declared policy
-- runs at its next scheduled time rather than immediately, and a daemon
-- that was down catches up on the most recent missed fire exactly once.
-- The table is disposable state: a reinstall starts fresh.
CREATE TABLE backup_schedules (
    environment_id UUID NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    policy         TEXT NOT NULL,
    last_fire_at   TIMESTAMPTZ NOT NULL,
    last_backup_id UUID REFERENCES backups (id) ON DELETE SET NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, policy)
);

-- +goose Down
DROP TABLE backup_schedules;
ALTER TABLE backups
    DROP COLUMN strategy,
    DROP COLUMN policy,
    DROP COLUMN trigger;
