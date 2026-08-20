-- +goose Up

-- Edge traffic sampled from Traefik's per-router Prometheus counters. Rows
-- carry per-interval deltas (the sampler differences the raw counters in
-- memory, so restarts and counter resets never produce negative spikes) and
-- keep the route key for a future per-route breakdown; the API sums per
-- application. Same retention discipline as the usage tables.
CREATE TABLE metric_edge_samples (
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    route_key       TEXT NOT NULL,
    sampled_at      TIMESTAMPTZ NOT NULL,
    requests        BIGINT NOT NULL,
    request_bytes   BIGINT NOT NULL,
    response_bytes  BIGINT NOT NULL,
    PRIMARY KEY (environment_id, application_key, route_key, sampled_at)
);
CREATE INDEX metric_edge_samples_sampled_at_idx ON metric_edge_samples (sampled_at);

-- +goose Down

DROP TABLE metric_edge_samples;
