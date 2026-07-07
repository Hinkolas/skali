-- +goose Up

-- The mirror catalog: images the master imported into the cluster registry,
-- digest-pinned from upstream. The master is the only writer (imports and
-- deletes go through it), so unlike the node_* observation tables this is
-- authoritative state, not a report mirror. Re-importing a (repository, tag)
-- moves the pin: same row, new digest.
CREATE TABLE registry_images (
    id          UUID PRIMARY KEY,
    repository  TEXT NOT NULL,               -- mirror-relative, e.g. mirror/docker.io/library/postgres
    tag         TEXT NOT NULL,
    digest      TEXT NOT NULL,               -- upstream manifest/index digest, preserved by the copy
    size_bytes  BIGINT NOT NULL DEFAULT 0,   -- unique blob bytes across the whole index
    imported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repository, tag)
);

-- +goose Down
DROP TABLE registry_images;
