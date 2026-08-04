-- +goose Up

-- The installation identity: minted once when this database is created and
-- never updated afterwards. Every API response carries it in the
-- Skali-Instance header so clients can pin it per remote and detect that a
-- cluster was uninstalled and reinstalled (which recreates this database and
-- therefore mints a new identity). It survives exactly what it should:
-- daemon restarts, upgrades, and backup restores. The singleton column keeps
-- the table at one row.
CREATE TABLE instance_identity (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    id        UUID NOT NULL
);
INSERT INTO instance_identity (id) VALUES (gen_random_uuid());

-- +goose Down
DROP TABLE instance_identity;
