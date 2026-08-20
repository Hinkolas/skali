-- +goose Up

-- Usage telemetry sampled from metrics.k8s.io by the skalid metrics sampler.
-- Rows are per-application aggregates (summed over the app's pods at sample
-- time), not per-pod: the console charts applications, and per-pod rows would
-- multiply the table for no consumer. Raw 30s samples only, pruned by age;
-- read queries bucket with date_bin, so a rollup table can slot in behind the
-- same reads later without an API change.
CREATE TABLE metric_app_samples (
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    sampled_at      TIMESTAMPTZ NOT NULL,
    cpu_millicores  BIGINT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    pod_count       BIGINT NOT NULL,
    PRIMARY KEY (environment_id, application_key, sampled_at)
);
CREATE INDEX metric_app_samples_sampled_at_idx ON metric_app_samples (sampled_at);

-- Allocatable is denormalized per sample so a node resize shows up in history
-- instead of rewriting it; the API serves the latest row's value as the
-- capacity scalar.
CREATE TABLE metric_node_samples (
    node_name                  TEXT NOT NULL,
    sampled_at                 TIMESTAMPTZ NOT NULL,
    cpu_millicores             BIGINT NOT NULL,
    memory_bytes               BIGINT NOT NULL,
    cpu_allocatable_millicores BIGINT NOT NULL,
    memory_allocatable_bytes   BIGINT NOT NULL,
    PRIMARY KEY (node_name, sampled_at)
);
CREATE INDEX metric_node_samples_sampled_at_idx ON metric_node_samples (sampled_at);

-- +goose Down

DROP TABLE metric_node_samples;
DROP TABLE metric_app_samples;
