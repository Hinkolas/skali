-- +goose Up

-- Protection bypass (skali deploy --bypass-protection): an environment
-- admin with a recent login may deploy into a promote-only environment
-- anyway. The fact is recorded on the deployment (the durable record) and
-- on its run (what listings and the console show), so it never depends on
-- the client telling it twice.
ALTER TABLE deployments ADD COLUMN bypass_protection BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE runs ADD COLUMN bypass_protection BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down

ALTER TABLE runs DROP COLUMN bypass_protection;
ALTER TABLE deployments DROP COLUMN bypass_protection;
