# 03 — Service model

## Concepts

- **Project** — the scope services live in. Maps to a k8s namespace (`prj-<slug>`).
  v1 is minimal: name + slug. Environments (prod/staging/…) are a *later* dimension
  inside projects — the model leaves room (see Later), v1 does not build it.
- **Service** — the core primitive; one typed row per user-facing thing. Types:
  `application`, `database` now; `cache`, `object_storage`, `static_site` reserved
  ("coming soon" in the UI). A service is intent; skali compiles it to k8s objects.
- **Release** — applications only: an immutable deploy record (digest-pinned image +
  spec snapshot). Enables history and rollback.
- **Database pool** — internal (not a service): one CNPG cluster hosting n logical
  databases. Database services reference a pool. Users normally never see pools;
  power users can request a dedicated one (05).
- **Connection** — a link app-service → database-service that injects connection env
  into the app (04, 05).

The old "workload" disappears as a user concept. What replaces its role internally:
`service` (intent) + compiled k8s objects (execution). There is deliberately **no
generic container service type** — raw containers are what kubectl is for; skali
only ships opinionated primitives.

## Data model (new baseline migrations)

```sql
projects (
  id uuid pk, name text, slug text unique,        -- slug: dns-label, immutable
  created_at, updated_at
)

services (
  id uuid pk, project_id fk, type text check (application|database|...),
  name text,                    -- display name, mutable
  slug text,                    -- dns-label, immutable, unique per project
  desired_state text check (running|stopped) default running,
  status text,                  -- cached rollup, controller-owned (see Status)
  status_detail text,           -- human-readable reason (controller-owned)
  last_error text, retries int, next_attempt_at timestamptz,  -- controller backoff
  created_at, updated_at,
  unique (project_id, slug)
)

service_applications (          -- 1:1 with services of type application
  service_id pk fk,
  image text,                   -- user intent, tag ref ("ghcr.io/x/app:1.4")
  port int, replicas int, cpu_millis int, memory_mb int,
  health_path text,
  env jsonb,                    -- [{name, value}]  (secret refs later)
  domains jsonb,                -- [{host, path}]
  volumes jsonb                 -- [{name, mount_path, size_mb}]
)

releases (
  id uuid pk, service_id fk,
  number bigint,                -- per-service sequence
  image_repository text, image_digest text,   -- resolved pin
  spec jsonb,                   -- full snapshot of service_applications at deploy
  status text check (pending|rolling_out|active|superseded|failed),
  created_by fk users, created_at
)

database_pools (
  id uuid pk, engine text check (postgres), major_version int,
  name text unique,             -- k8s name in skali-dbs ns
  dedicated_project_id fk null, -- null = shared pool
  instances int default 1, cpu_millis int, memory_mb int, storage_gb int,
  status text, created_at, updated_at
)

service_databases (             -- 1:1 with services of type database
  service_id pk fk,
  pool_id fk database_pools,
  engine text, major_version int,
  db_name text, db_user text,   -- generated, dns/sql-safe
  secret_name text,             -- k8s Secret holding the password (skali-dbs ns)
  extensions jsonb,             -- ["pgvector", "postgis"]
  storage_soft_limit_mb int null
)

connections (
  id uuid pk, app_service_id fk, db_service_id fk,
  env_prefix text default 'DATABASE',   -- DATABASE_URL, DATABASE_HOST, ...
  created_at, unique (app_service_id, db_service_id)
)
```

Conventions carried over: UUIDv7, goose baseline (auth migrations 00001–00003 keep
their numbers; everything after is rewritten), sqlc typed queries, JSONB only for
open-ended lists.

**Credentials rule:** database passwords live in **k8s Secrets only**; skali's
Postgres stores the secret *name*. UI reveal/copy reads the Secret via the kube API
on demand. No plaintext credentials in skali's DB, no second crypto path
(`internal/crypt` stays for TOTP only).

## Compilation contract

Per type, a pure function in `internal/compile`:

```
Compile(project, service, typed detail, [release|pool]) → []Manifest
```

- application → Deployment (+ PVCs), Service, Ingress(es)         (04)
- database    → CNPG Database CR + managed role + Secret          (05)
- pool        → CNPG Cluster CR (+ image catalog ref)             (05)

Rules: deterministic, no I/O, no time; everything the controller applies carries the
label schema from 02. Golden-file tests per compiler (`testdata/*.yaml`) are the
primary test surface of the whole backend.

## Status model

`desired_state` is user intent (running|stopped). `status` is a controller-cached
rollup, same philosophy as the old `rollupStatus`:

```
provisioning   objects applied, not yet available (first rollout / pool not ready)
running        available at desired generation
deploying      a release is rolling out (apps)
degraded       available < desired (some replicas down, pool not healthy)
stopped        desired_state=stopped and converged
error          controller retry-looping (see last_error)
deleting       teardown in progress
```

Derivation: applications from Deployment conditions + release status; databases from
CNPG Cluster phase + Database CR readiness. The web polls the API exactly as the
workloads page does today (fast while converging, slow when settled).

## API surface (replaces /v1/workloads)

```
GET/POST            /v1/projects
GET/PATCH/DELETE    /v1/projects/{project}
GET/POST            /v1/projects/{project}/services        (POST body is typed)
GET/PATCH/DELETE    /v1/projects/{project}/services/{slug}
POST                /v1/projects/{project}/services/{slug}/deploy     (apps: new release)
POST                .../services/{slug}/actions   {action: start|stop|restart|rollback}
GET                 .../services/{slug}/releases
GET                 .../services/{slug}/connection            (db: host/port/db/user + password via secret read)
GET/POST/DELETE     .../services/{slug}/connections           (app↔db links)
GET                 .../services/{slug}/logs?follow=1         (apps: pod logs proxy)
GET                 /v1/nodes                                 (read-only, from kube API)
```

Same middleware stack as today (admin + sudo-gated writes for now; per-project
membership is a Later item). OpenAPI stays the embedded contract. Slugs in paths —
they're the k8s names and the UI routes (`projects/[project]/services/[service]`
already expects slugs).

## Deletion semantics

`DELETE` marks the service `deleting` (202, as today); the controller prunes owned
objects, waits for them to go, then hard-deletes rows. Databases: deleting the
service drops the logical database (Database CR with delete reclaim) after an
explicit UI confirmation naming the data loss; pools are garbage-collected only when
empty and shared, never implicitly for dedicated pools.

## Later (explicitly out of v1, model leaves room)

- **Environments**: a column on services (or a project sub-scope) + CNPG clone for
  data duplication. Do not build half of it now.
- **Per-project members/roles**, service accounts + API tokens for CLI/CI.
- **cache / object_storage / static_site** types: same table pattern
  (`service_caches`, …), same compiler contract.
- **Compose import + templates**: producers of the same service rows — input
  formats, not new runtimes.
