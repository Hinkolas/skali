-- +goose Up

-- Storage telemetry sampled every few minutes by the skalid metrics sampler
-- (storage moves slowly, so the cadence is decoupled from the 30s usage
-- tick). Node rows carry the filesystem totals plus the attributable
-- category rollups the console's capacity bar segments on; the remainder up
-- to used_bytes is the system share, computed at read time. Same retention
-- discipline as the usage tables.
CREATE TABLE metric_storage_node_samples (
    node_name       TEXT NOT NULL,
    sampled_at      TIMESTAMPTZ NOT NULL,
    capacity_bytes  BIGINT NOT NULL,
    used_bytes      BIGINT NOT NULL,
    available_bytes BIGINT NOT NULL,
    volumes_bytes   BIGINT NOT NULL,
    databases_bytes BIGINT NOT NULL,
    objects_bytes   BIGINT NOT NULL,
    images_bytes    BIGINT NOT NULL,
    PRIMARY KEY (node_name, sampled_at)
);
CREATE INDEX metric_storage_node_samples_sampled_at_idx ON metric_storage_node_samples (sampled_at);

-- Per-service storage footprint: application volumes, databases, and
-- buckets, keyed by the environment and the product service key
-- (applications bare, databases and buckets prefixed like their status
-- identities). used_bytes is NULL where usage is unmeasurable (local-path
-- app volumes report whole-node statfs, which the sampler discards);
-- capacity_bytes is the declared size or quota, 0 when unknown.
CREATE TABLE metric_storage_samples (
    environment_id UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_key    TEXT NOT NULL,
    kind           TEXT NOT NULL,
    sampled_at     TIMESTAMPTZ NOT NULL,
    used_bytes     BIGINT,
    capacity_bytes BIGINT NOT NULL,
    PRIMARY KEY (environment_id, service_key, kind, sampled_at)
);
CREATE INDEX metric_storage_samples_sampled_at_idx ON metric_storage_samples (sampled_at);

-- +goose Down

DROP TABLE metric_storage_samples;
DROP TABLE metric_storage_node_samples;
