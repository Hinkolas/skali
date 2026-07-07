-- +goose Up

-- Observed image/volume inventory per node, upserted from heartbeat reports
-- (and the master's local sampler). Unlike node_containers this is
-- UNFILTERED — whole-node disk visibility, skali-managed or not — and rows
-- for images/volumes a present report omits are DELETED, not marked gone: a
-- vanished container is an operational signal worth a breadcrumb, inventory
-- rows just mirror what is on the node. An ABSENT report means unknown
-- (engine unreachable, sampler cold) and leaves rows untouched.
CREATE TABLE node_images (
    node_id       UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    image_id      TEXT NOT NULL,                  -- content-addressable sha256:…
    repo_tags     TEXT[] NOT NULL DEFAULT '{}',
    repo_digests  TEXT[] NOT NULL DEFAULT '{}',
    size_bytes    BIGINT NOT NULL,
    dangling      BOOLEAN NOT NULL,               -- derived at write: no repo tags
    -- Containers referencing the image, any owner or state; 0 = unused.
    containers    INT NOT NULL DEFAULT 0,
    image_created TIMESTAMPTZ,                    -- the image's own build time
    first_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, image_id)
);

-- No sizes: only the daemon's system-df walk computes volume sizes, far too
-- expensive for a sampler.
CREATE TABLE node_volumes (
    node_id        UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,                 -- anonymous 64-hex names included
    driver         TEXT NOT NULL,
    scope          TEXT NOT NULL,                 -- local|global
    mountpoint     TEXT NOT NULL,
    labels         JSONB NOT NULL DEFAULT '{}',
    -- Containers mounting the volume, any owner or state; 0 = unused.
    containers     INT NOT NULL DEFAULT 0,
    volume_created TIMESTAMPTZ,
    first_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, name)
);

-- +goose Down
DROP TABLE node_volumes;
DROP TABLE node_images;
