# 05 — Data Model & Storage

## Principles

- **SQLite on the master is the single source of truth.** Workers store no
  authoritative state; their running containers are reconciled toward this DB.
- **Go owns the database 100%.** The web UI is `adapter-static` (no server
  runtime), so there is no Drizzle/Node DB layer. The daemon is a single binary
  that **migrates its own DB at startup** with no external tooling.
- **Driver:** `modernc.org/sqlite` (pure-Go, **no cgo**) so both binaries
  cross-compile cleanly.
- **Migrations:** **goose**, SQL migrations embedded via `embed.FS`, applied with
  `goose.Up()` on `skalid start`. Chosen over golang-migrate to avoid its
  "dirty database" lockout (bad for auto-migrate-on-boot on user servers) and to
  allow **Go migrations** for future data transforms (e.g. re-encrypting
  secrets). Authoring is by hand; if diff-generation DX becomes desirable, Atlas
  can be layered in as an *authoring* tool later, but goose stays the single
  runtime applier.
- **Queries:** **sqlc** generates type-safe Go from SQL. Note SQLite's dynamic
  typing occasionally needs nullability hints in sqlc — expected, manageable.

## Entity overview

```
User ──< Token
Node ──< Instance
Registry ──< Project
Project ──< Environment ──< Release ──< Instance
Project ──< Build
Environment ──< Route
Environment ──< EnvVar
JoinToken            (one-time node enrollment secrets)
ClusterCA            (the cluster's CA key/cert; one row)
```

Relationships in words:

- A **Project** is the *build* unit: it belongs to a Registry (the built-in
  managed registry by default; a connected external one optionally — see `04`),
  holds the build config, and has many Builds and one or more Environments.
- An **Environment** is a named *running configuration* of a Project
  (`production`, `staging`, …): its own Routes, EnvVars, replicas, placement,
  egress, and resource limits, plus its own Release history. Every Project gets a
  default Environment (`production`) implicitly, so simple single-env projects
  never have to think about this (progressive disclosure). Multi-environment UX
  (`skali env create`, `skali promote`) is `[soon]`; the **dimension is in the
  schema from 0.1.0** so it never needs retrofitting.
- A **Build** is `(source) → image digest`, owned by the Project (env-agnostic).
  Because env is injected at **runtime**, one Build's image normally serves every
  Environment — a `promote` reuses the digest, never rebuilds (`04`). The
  exception is build-baked config (e.g. `NEXT_PUBLIC_*`): a Project flagged
  `build_per_env` builds a separate image per Environment instead.
- A **Release** is an immutable desired-state snapshot for an **Environment**
  (image digest + that env's snapshot + replica/placement intent). The "current"
  Release is the desired version *for that environment*.
- An **Instance** is one running container realizing a Release on a Node (actual
  runtime, reconciled).
- A **Node** has roles and addressing; it hosts Instances.

## Schema sketch (illustrative DDL)

> Illustrative, not final. IDs are text (ULID/UUID) for client-friendliness;
> timestamps are unix or RFC3339 text. Trim/add columns during implementation.

```sql
CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,           -- argon2id
  role          TEXT NOT NULL DEFAULT 'admin',  -- RBAC stub; see 08
  created_at    TEXT NOT NULL
);

CREATE TABLE tokens (               -- opaque user/CLI tokens (see 08)
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  hash        TEXT NOT NULL,        -- store only a hash of the token
  name        TEXT,                 -- e.g. device/CI label
  kind        TEXT NOT NULL,        -- 'web' | 'cli'
  last_used   TEXT,
  expires_at  TEXT,                 -- nullable = long-lived PAT
  revoked_at  TEXT,
  created_at  TEXT NOT NULL
);

CREATE TABLE nodes (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  roles          TEXT NOT NULL,     -- csv/json: master,edge,worker,builder
  arch           TEXT,              -- 'amd64' | 'arm64' — reported by the node's Docker engine (07)
  advertise_addr TEXT NOT NULL,     -- peer-facing address (private net)
  public_addr    TEXT,              -- edge role only
  proxy_port     INTEGER NOT NULL DEFAULT 7080,
  build_concurrency INTEGER NOT NULL DEFAULT 1,  -- max concurrent builds (builder role); scheduler cap
  cert_serial    TEXT,              -- current mTLS identity (CA-signed)
  status         TEXT NOT NULL,     -- enrolling|ready|unreachable|disabled
  last_seen      TEXT,
  created_at     TEXT NOT NULL
);

CREATE TABLE registries (
  id        TEXT PRIMARY KEY,
  kind      TEXT NOT NULL,          -- 'builtin' (default, managed registry:2) | 'external'
  endpoint  TEXT NOT NULL,
  username  TEXT,
  secret_enc BLOB,                  -- plaintext in 0.1.0; encrypted at rest [soon]
  created_at TEXT NOT NULL
);

CREATE TABLE projects (             -- the BUILD unit (env-agnostic)
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  slug          TEXT NOT NULL UNIQUE,
  registry_id   TEXT NOT NULL REFERENCES registries(id),
  build_per_env INTEGER NOT NULL DEFAULT 0,  -- 1 = build-baked config; image not shareable across envs (04)
  created_at    TEXT NOT NULL
);

CREATE TABLE environments (         -- a named running configuration of a project
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,        -- 'production' (default) | 'staging' | ...
  is_default  INTEGER NOT NULL DEFAULT 0,  -- the env `skali deploy` targets with no --env
  replicas    INTEGER NOT NULL DEFAULT 1,
  placement   TEXT,                 -- json: target node selector(s)
  egress_mode TEXT NOT NULL DEFAULT 'internet', -- internet|isolated
  resources   TEXT,                 -- json: cpu/memory/upload limits ([soon] enforcement)
  env_file    TEXT,                 -- committed config's mapped .env path, for CLI var sync (11)
  created_at  TEXT NOT NULL,
  UNIQUE(project_id, name)
);

CREATE TABLE routes (               -- host rules attached to an ENVIRONMENT
  id            TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  host          TEXT NOT NULL,      -- e.g. staging.example.com / example.com
  listen_port   INTEGER NOT NULL DEFAULT 443,  -- EXTERNAL entrypoint port (part of the routing key)
  path_prefix   TEXT NOT NULL DEFAULT '',      -- '' = match all; lets many services share one host
  service_port  INTEGER NOT NULL,   -- INTERNAL container port to forward to (the target, not a key)
  tls           INTEGER NOT NULL DEFAULT 1,
  created_at    TEXT NOT NULL,
  -- cluster-wide routing-key uniqueness: one (host, external port, path) maps to one service.
  -- path_prefix is NOT NULL DEFAULT '' so the unique index treats "match-all" as a real value.
  UNIQUE(host, listen_port, path_prefix)
);

CREATE TABLE env_vars (             -- variables, keyed by ENVIRONMENT
  id             TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  key            TEXT NOT NULL,
  value_enc      BLOB NOT NULL,     -- plaintext in 0.1.0; encrypted at rest [soon]
  is_secret      INTEGER NOT NULL DEFAULT 1,
  UNIQUE(environment_id, key)
);

CREATE TABLE builds (             -- the build JOB QUEUE; master is the scheduler (see 04)
  id            TEXT PRIMARY KEY,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_ref    TEXT,               -- 'cli-upload:<hash>' | 'cli-local:<hash>' | git sha | image ref
  build_location TEXT NOT NULL DEFAULT 'node', -- 'node' | 'local' (built on a client by skali CLI)
  target_platform TEXT NOT NULL DEFAULT 'linux/amd64', -- arch the image is for; needs a matching builder (0.1.0: native only)
  builder_node  TEXT REFERENCES nodes(id),     -- assigned builder; NULL when 'local' or still queued
  image_digest  TEXT,               -- set on success (for local builds, recorded after the client pushes)
  status        TEXT NOT NULL,      -- queued|building|pushing|succeeded|failed
  attempts      INTEGER NOT NULL DEFAULT 0,     -- bounded retries on builder death/timeout
  priority      INTEGER NOT NULL DEFAULT 0,     -- scheduler ordering (higher first), else FIFO by created_at
  logs_ref      TEXT,               -- where build logs are kept (node builds; local prints client-side)
  created_at    TEXT NOT NULL,
  finished_at   TEXT
);

CREATE TABLE releases (             -- immutable desired-state versions, per ENVIRONMENT
  id             TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  build_id       TEXT REFERENCES builds(id),
  image_digest   TEXT NOT NULL,     -- what actually runs; pinned digest (may be promoted from another env)
  env_snapshot   BLOB NOT NULL,     -- this env's vars at release time; plaintext in 0.1.0, encrypted [soon]
  replicas       INTEGER NOT NULL,
  placement      TEXT,
  promoted_from  TEXT REFERENCES releases(id),  -- set when this release reused another env's digest (04)
  created_at     TEXT NOT NULL,
  is_current     INTEGER NOT NULL DEFAULT 0   -- the desired version for this environment
);

CREATE TABLE instances (            -- actual running containers
  id            TEXT PRIMARY KEY,
  release_id    TEXT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  node_id       TEXT NOT NULL REFERENCES nodes(id),
  container_id  TEXT,               -- docker container id
  status        TEXT NOT NULL,      -- pending|starting|healthy|unhealthy|stopped
  health        TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE join_tokens (          -- one-time node enrollment secrets (see 08)
  id         TEXT PRIMARY KEY,
  hash       TEXT NOT NULL,
  roles      TEXT NOT NULL,         -- roles to grant on enrollment
  expires_at TEXT NOT NULL,
  used_at    TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE cluster_ca (           -- single row: the cluster CA
  id          INTEGER PRIMARY KEY CHECK (id = 1),
  cert_pem    TEXT NOT NULL,
  key_enc     BLOB NOT NULL,        -- CA private key; plaintext in 0.1.0, encrypted [soon]
  created_at  TEXT NOT NULL
);
```

## Desired vs actual in the schema

- **Desired state** = the `is_current` `releases` across all `environments` +
  their `routes` + `replicas` / `placement`. Each Environment converges
  independently (staging and production roll out on their own schedules).
- **Actual state** = `instances` (+ live container status reported by workers).
- The reconciler (`03`) diffs the two and converges. `instances` is the master's
  *cache* of actual state, refreshed from worker reports — the worker's Docker is
  the ultimate truth for "is it running," reconciled against the DB's desired.

## Secrets at rest ([soon] — deferred out of 0.1.0)

> **Decision:** encryption at rest is **deferred** to a dedicated security pass
> after 0.1.0. We acknowledge the risk now so the schema and code barely change
> when we add it.

- **0.1.0 stores sensitive values as plaintext** in SQLite: env var values,
  registry credentials, the CA private key, and release env snapshots. A stolen
  DB file would leak them. This is an accepted, documented 0.1.0 limitation —
  protect the DB file with filesystem permissions and host security for now.
- **[soon] security pass:** wrap the sensitive columns with authenticated
  encryption (XChaCha20-Poly1305 / AES-GCM), keyed by an operator-provided master
  key that is **not** stored in the DB (env var / `--key-file`), so DB-at-rest
  theft alone won't reveal secrets. The columns are designed to hold either
  plaintext (now) or ciphertext (later), so this is an additive migration.
- **[future]** external secret providers (Vault, cloud KMS) behind the same
  interface; the master key becomes a KMS-wrapped key.

## Concurrency & integrity

- SQLite in **WAL mode**, single writer (the master). Fine for this scale.
- **Per-environment deploy lock** (advisory, in the DB or in-process) so two
  concurrent deploys/promotes targeting the same Environment serialize rather than
  racing the reconciler. Different environments of one project deploy
  concurrently.
- Foreign keys **ON**; cascade deletes for owned children.
- Backups: the DB file under the data dir; document `litestream` as an optional
  continuous-backup add-on (also a stepping stone toward `[future]` master HA).
- **Ephemeral build-context blobs** also live under the data dir, content-addressed
  by the `cli-upload:<hash>` in `builds.source_ref` — written on upload, fetched by
  the assigned builder, GC'd at terminal build state (`04`). Not authoritative
  state; excluded from backups.
