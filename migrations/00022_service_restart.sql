-- +goose Up

-- Per-application restart stamps: a service restart stamps one application,
-- while deploy --force keeps stamping the whole environment on
-- environment_targets.restarted_at. The reconciler renders the later of the
-- two stamps as the pod-template restart annotation, so both compose and a
-- service restart rolls exactly one workload. Stateful services never read
-- either stamp: restarts recreate pods, never data.
CREATE TABLE environment_restarts (
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    restarted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, application_key)
);

-- +goose Down

DROP TABLE environment_restarts;
