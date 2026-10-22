-- +goose Up

-- One stretch of time during which a pool refused new ordinary
-- connections: the connection check could not log in as an ordinary role
-- while the pool's instances were running. The substrate's connection
-- monitor opens the row when the refusal is confirmed and closes it only
-- after the check logs in again, so an open row survives a daemon restart
-- and recovery is never assumed. Notification delivery reads the identity
-- and timing from here; the live state is the monitor's.
CREATE TABLE pool_connection_incidents (
    id         UUID PRIMARY KEY,
    cluster_id UUID NOT NULL REFERENCES database_clusters(id) ON DELETE CASCADE,
    code       TEXT NOT NULL,
    message    TEXT NOT NULL,
    opened_at  TIMESTAMPTZ NOT NULL,
    closed_at  TIMESTAMPTZ,
    CHECK (closed_at IS NULL OR closed_at >= opened_at)
);
-- At most one open incident per pool.
CREATE UNIQUE INDEX pool_connection_incidents_open_key
    ON pool_connection_incidents (cluster_id) WHERE closed_at IS NULL;
CREATE INDEX pool_connection_incidents_cluster_idx
    ON pool_connection_incidents (cluster_id, opened_at DESC);

-- +goose Down
DROP TABLE pool_connection_incidents;
