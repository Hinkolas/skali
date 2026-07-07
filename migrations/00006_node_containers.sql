-- +goose Up

-- Observed container state per node, upserted from heartbeat reports (and
-- the master's local sampler). OBSERVED only — desired state arrives with
-- the application layer; until then the engine's restart policies carry
-- containers across reboots and this table is the master's (and the UI's)
-- view of reality. Latest-only, no history.
--
-- A present report is authoritative for its node: containers it omits flip
-- to state 'gone' (kept briefly as operator breadcrumbs, pruned by the
-- poller). An ABSENT report means unknown — engine unreachable, sampler
-- cold — and leaves rows untouched.
CREATE TABLE node_containers (
    node_id           UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    container_id      TEXT NOT NULL,              -- engine id (64-hex)
    name              TEXT NOT NULL,
    image             TEXT NOT NULL,
    -- Management kind, extracted from the skali.kind label at write time so
    -- the future layers (and the UI) can query it directly.
    kind              TEXT NOT NULL CHECK (kind IN ('application', 'database', 'system')),
    state             TEXT NOT NULL,              -- engine states + 'gone'
    health            TEXT,                       -- NULL = no healthcheck
    exit_code         INT,
    restart_count     INT NOT NULL DEFAULT 0,
    labels            JSONB NOT NULL DEFAULT '{}',
    container_created TIMESTAMPTZ,                -- the container's own timestamps
    container_started TIMESTAMPTZ,
    -- Latest stats; NULL until the node's container sampler has two samples.
    -- cpu_pct uses docker-stats semantics: 100 = one full core.
    cpu_pct           REAL,
    mem_used          BIGINT,
    mem_limit         BIGINT,
    net_rx_rate       BIGINT,                     -- bytes/second
    net_tx_rate       BIGINT,
    first_seen        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, container_id)
);

-- +goose Down
DROP TABLE node_containers;
