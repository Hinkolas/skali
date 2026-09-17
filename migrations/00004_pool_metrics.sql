-- +goose Up

-- Pool usage telemetry, sampled every tick by the skalid metrics sampler:
-- the summed PodMetrics of a pool's instances plus the primary's CNPG
-- exporter. Keyed by the cluster row, not the name: shared pool names recur
-- across release and recreate and their histories must not splice. The
-- exporter columns are NULL when the scrape failed; the counter deltas are
-- NULL on the first sighting and after a counter reset (stats reset,
-- failover moving the scrape to another instance).
CREATE TABLE metric_pool_samples (
    cluster_id      UUID NOT NULL REFERENCES database_clusters(id) ON DELETE CASCADE,
    sampled_at      TIMESTAMPTZ NOT NULL,
    cpu_millicores  BIGINT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    instances       BIGINT NOT NULL,
    instances_ready BIGINT NOT NULL,
    connections     BIGINT,
    xact_commit     BIGINT,
    xact_rollback   BIGINT,
    blks_hit        BIGINT,
    blks_read       BIGINT,
    database_bytes  BIGINT,
    PRIMARY KEY (cluster_id, sampled_at)
);
CREATE INDEX metric_pool_samples_sampled_at_idx ON metric_pool_samples (sampled_at);

-- +goose Down
DROP TABLE metric_pool_samples;
