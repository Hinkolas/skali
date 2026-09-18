-- +goose Up

-- Automatic backups are an environment setting, not a manifest policy: one
-- optional schedule per environment (five-field cron in UTC), the retention
-- of the snapshots it takes, and the strategy (complete is the only one).
-- An empty schedule means automatic backups are off.
ALTER TABLE environments
    ADD COLUMN backup_schedule          TEXT   NOT NULL DEFAULT '',
    ADD COLUMN backup_retention_seconds BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN backup_strategy          TEXT   NOT NULL DEFAULT 'complete'
        CHECK (backup_strategy IN ('complete'));

-- Snapshots a manifest backup policy took belong to nobody now: they read
-- as manual snapshots and retention never touches them. This rewrite is
-- one way; Down cannot recover which rows a policy produced. Scheduled rows
-- carry the retention that was in force when the run was accepted, so the
-- run journal and the retention step agree even if the setting changes
-- while the run is pending.
UPDATE backups SET trigger = 'manual', policy = '' WHERE trigger = 'scheduled';
ALTER TABLE backups
    DROP COLUMN policy,
    ADD COLUMN retention_seconds BIGINT NOT NULL DEFAULT 0;

-- Scheduler state is keyed per environment. schedule records the expression
-- the row was seeded for, so a changed expression reseeds at the next fire
-- instead of catching up on the old one. Every schedule is off after this
-- migration until an environment admin turns it on.
TRUNCATE backup_schedules;
ALTER TABLE backup_schedules
    DROP CONSTRAINT backup_schedules_pkey,
    DROP COLUMN policy,
    ADD COLUMN schedule TEXT NOT NULL,
    ADD PRIMARY KEY (environment_id);

-- +goose Down
TRUNCATE backup_schedules;
ALTER TABLE backup_schedules
    DROP CONSTRAINT backup_schedules_pkey,
    DROP COLUMN schedule,
    ADD COLUMN policy TEXT NOT NULL DEFAULT '';
ALTER TABLE backup_schedules ALTER COLUMN policy DROP DEFAULT;
ALTER TABLE backup_schedules ADD PRIMARY KEY (environment_id, policy);
ALTER TABLE backups
    DROP COLUMN retention_seconds,
    ADD COLUMN policy TEXT NOT NULL DEFAULT '';
ALTER TABLE environments
    DROP COLUMN backup_strategy,
    DROP COLUMN backup_retention_seconds,
    DROP COLUMN backup_schedule;
