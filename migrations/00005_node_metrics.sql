-- +goose Up

-- Latest resource snapshot per node, stamped by the heartbeat poller. All
-- nullable: absent until the node's sampler has reported once. Typed columns
-- (not JSONB) so the future scheduler can query them directly.
ALTER TABLE nodes
    ADD COLUMN cpu_pct         REAL,
    ADD COLUMN mem_used        BIGINT,
    ADD COLUMN mem_total       BIGINT,
    ADD COLUMN disk_used       BIGINT,
    ADD COLUMN disk_total      BIGINT,
    ADD COLUMN net_rx_rate     BIGINT,   -- bytes/second
    ADD COLUMN net_tx_rate     BIGINT,
    ADD COLUMN disk_read_rate  BIGINT,
    ADD COLUMN disk_write_rate BIGINT,
    ADD COLUMN load1           REAL;

-- Metrics history: one row per successful heartbeat (~15s cadence), retained
-- 24h (pruned by the poller's hygiene sweep). Rows exist ONLY for successful
-- heartbeats — offline windows are absent rows, never zero-filled, so charts
-- render them as gaps. The API reads this bucketed (date_bin + avg), never raw.
CREATE TABLE node_metrics (
    node_id         UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    sampled_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    cpu_pct         REAL NOT NULL,
    mem_used        BIGINT NOT NULL,
    mem_total       BIGINT NOT NULL,
    disk_used       BIGINT NOT NULL,
    disk_total      BIGINT NOT NULL,
    net_rx_rate     BIGINT NOT NULL,
    net_tx_rate     BIGINT NOT NULL,
    disk_read_rate  BIGINT NOT NULL,
    disk_write_rate BIGINT NOT NULL,
    load1           REAL NOT NULL
);

CREATE INDEX node_metrics_node_time ON node_metrics (node_id, sampled_at);

-- +goose Down
DROP TABLE node_metrics;
ALTER TABLE nodes
    DROP COLUMN cpu_pct,
    DROP COLUMN mem_used,
    DROP COLUMN mem_total,
    DROP COLUMN disk_used,
    DROP COLUMN disk_total,
    DROP COLUMN net_rx_rate,
    DROP COLUMN net_tx_rate,
    DROP COLUMN disk_read_rate,
    DROP COLUMN disk_write_rate,
    DROP COLUMN load1;
