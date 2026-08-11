-- +goose Up

-- Applications a dev session runs on the host instead of in the cluster.
-- Each deploy replaces the environment's whole set: a deploy without local
-- applications clears it, which is exactly what skali dev --preview and
-- plain skali deploy do. ports maps the application's service port names to
-- the host ports the local dev process listens on.
CREATE TABLE environment_intercepts (
    environment_id  UUID NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    application_key TEXT NOT NULL,
    ports           JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, application_key)
);

-- The requested intercept set travels on the deployment row so Complete can
-- promote it transactionally without trusting the client a second time.
ALTER TABLE deployments ADD COLUMN local_applications JSONB;

-- Loopback NodePort of the pool's primary Service on the local platform.
-- Always NULL on managed clusters. The partial unique index frees the port
-- as soon as a pool is released.
ALTER TABLE database_clusters ADD COLUMN node_port INTEGER;
CREATE UNIQUE INDEX database_clusters_node_port
    ON database_clusters (node_port)
    WHERE node_port IS NOT NULL AND state <> 'released';

-- +goose Down

DROP INDEX database_clusters_node_port;
ALTER TABLE database_clusters DROP COLUMN node_port;
ALTER TABLE deployments DROP COLUMN local_applications;
DROP TABLE environment_intercepts;
