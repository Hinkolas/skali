-- +goose Up

-- Desired state: one container spec + placement, declared on the master and
-- converged by the reconciler. Authoritative like registry_images, never a
-- report mirror. `image` is the upstream reference the user asked for;
-- resolved_repository/resolved_digest are reconciler output — the mirror pin
-- containers actually run, owned by the workload (a re-import that moves the
-- catalog pin does not restart workloads; picking it up is an explicit
-- update). generation bumps on every user update so assignments can report
-- which revision they converged to.
CREATE TABLE workloads (
    id              UUID PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,        -- also the container name stem: <name>-<ordinal>
    kind            TEXT NOT NULL DEFAULT 'application'
                    CHECK (kind IN ('application', 'database')),
    desired_state   TEXT NOT NULL DEFAULT 'running'
                    CHECK (desired_state IN ('running', 'stopped', 'deleting')),
    replicas        INT  NOT NULL DEFAULT 1 CHECK (replicas >= 0),
    constraints     JSONB NOT NULL DEFAULT '{}', -- {"node_ids":[…],"node_roles":[…]}; empty = any node
    image           TEXT NOT NULL,               -- upstream tag reference, e.g. postgres:17
    spec            JSONB NOT NULL DEFAULT '{}', -- container spec subset (internal/reconcile.Spec)
    generation      BIGINT NOT NULL DEFAULT 1,
    resolved_repository TEXT,
    resolved_digest     TEXT,
    -- Workload-level failure surface (image resolution); per-node failures
    -- live on the assignments.
    last_error      TEXT,
    retries         INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Reconciler-owned placement slots: exactly one row per replica ordinal.
-- node_id is reconciler output, never user input; NULL means unplaced —
-- fresh or unschedulable, last_error says which. ON DELETE SET NULL frees a
-- deleted node's slots for re-placement: the schema already supports
-- rescheduling, v1 just never un-assigns on its own. There is deliberately
-- no 'error' phase: an error is last_error + next_attempt_at on whatever
-- phase is stuck, which preserves where it failed.
CREATE TABLE workload_assignments (
    workload_id     UUID NOT NULL REFERENCES workloads(id) ON DELETE CASCADE,
    ordinal         INT  NOT NULL CHECK (ordinal >= 0),
    node_id         UUID REFERENCES nodes(id) ON DELETE SET NULL,
    phase           TEXT NOT NULL DEFAULT 'pending'
                    CHECK (phase IN ('pending', 'unschedulable', 'pulling',
                                     'deploying', 'stopping', 'ready', 'stopped', 'removing')),
    container_name  TEXT NOT NULL,               -- deterministic: <workload-name>-<ordinal>
    container_id    TEXT,                        -- last created/observed engine id
    generation      BIGINT NOT NULL DEFAULT 0,   -- workload generation last converged to
    retries         INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,                 -- backoff gate; NULL = eligible now
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workload_id, ordinal)
);

-- The reconciler reads observed containers by ownership label.
CREATE INDEX node_containers_workload_idx
    ON node_containers ((labels->>'skali.workload'));

-- +goose Down
DROP INDEX node_containers_workload_idx;
DROP TABLE workload_assignments;
DROP TABLE workloads;
