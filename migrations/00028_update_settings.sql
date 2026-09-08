-- +goose Up

-- Console-driven platform updates: the operator's channel and auto-update
-- choice plus the result of the last release scan, one row per
-- installation (the singleton column keeps it at one, seeded here so reads
-- never miss). What is installed is not recorded: the running daemon knows
-- its own version and the cluster state knows every node's, so this row
-- only remembers what was found upstream and when, which is what a page
-- load must answer without a network round trip.
CREATE TABLE update_settings (
    singleton              BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    channel                TEXT NOT NULL DEFAULT 'stable' CHECK (channel IN ('stable', 'beta')),
    auto_update            BOOLEAN NOT NULL DEFAULT FALSE,
    last_checked_at        TIMESTAMPTZ,
    latest_version         TEXT,
    latest_k3s             TEXT,
    latest_published_at    TIMESTAMPTZ,
    latest_url             TEXT,
    last_error             TEXT,
    last_error_kind        TEXT CHECK (last_error_kind IN ('offline', 'not_found', 'rate_limited', 'unavailable', 'invalid')),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO update_settings DEFAULT VALUES;

-- +goose Down
DROP TABLE update_settings;
