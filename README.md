# skali

Self-hostable hosting platform: deploy an app from a Dockerfile to a live
HTTPS URL on your own hardware, with managed Postgres and quota-enforced
volumes declared alongside it. The technical foundation mirrors the smbx
architecture (Go + chi + Postgres, one token-based API for every client,
SvelteKit BFF); where skali is headed is the [Roadmap](#roadmap) below.

**Status:** milestone 4 — registry mirror + event stream (a cluster image
registry as a skali-managed system container, digest-pinned imports,
doorbell-driven resyncs; on top of milestone 3's container engine and
milestone 2's multi-node clusters). One API (`/v1`), three consumers:

- **`skalid`** — the daemon. On the master it serves the REST API and the
  cluster control plane; on other machines it runs as a worker (`agent`).
  Also carries the operator commands (`user`, `migrate`, `enroll`).
- **`skali`** — the CLI. A pure REST client with kubectl-style contexts.
- **`web/`** — SvelteKit BFF (adapter-node). Owns the browser session cookie
  and proxies `/api/v1/*` to the daemon; the bearer token never reaches
  browser JavaScript. Later it runs as a skalid-managed container.

Auth is email+password (argon2id) with optional TOTP 2FA and backup codes;
sessions are opaque bearer tokens (sha256-hashed at rest, 30-day sliding
expiry, instant revocation). There is no signup endpoint — users are created
by the operator.

## Quickstart (dev)

Requirements: Go 1.26+, Node 22+, [go-task](https://taskfile.dev), Docker
(for the shared dev Postgres), sqlc (only when changing queries).

The control-plane database is an **external Postgres by default**: skalid
connects to whatever `DATABASE_URL` points at (managed DB, host install, or
the machine-global shared dev container from `~/Taskfile.yml`). Later, skalid
provisions its own Postgres through its container engine as the main
deployment path, keeping the external database as the option for dev setups
and managed services.

```sh
# 1. Environment (DATABASE_URL, AUTH_SECRET, …)
cp .env.example .env    # then set AUTH_SECRET: openssl rand -base64 32

# 2. Shared dev Postgres + skali's database (idempotent)
task db

# 3. Migrate + create the admin user
go run ./cmd/skalid migrate up
go run ./cmd/skalid user create --email you@example.com

# 4. Run the API (:7070) — ensures the dev db first
task dev

# 5. CLI
go run ./cmd/skali auth login --master http://localhost:7070
go run ./cmd/skali auth whoami

# 6. Web UI (vite dev server on :5173, BFF → API)
cd web && cp .env.example .env && npm install
task dev:web
```

The OpenAPI contract is served at `GET /openapi.yaml` and lives in
[`api/openapi.yaml`](api/openapi.yaml); a router-walk test keeps it honest.

## Adding a node

One `skalid` master per cluster; other machines join as workers over a
mTLS gRPC control plane (master dials workers; only the master needs to be
reachable by new nodes at enrollment).

```sh
# On the master: set CLUSTER_ADDR (the externally reachable gRPC address,
# e.g. 10.0.0.1:7443) in the environment, then mint a join token in the web
# UI (Nodes → Add node) — it renders the full command for the new machine:

# On the new machine:
skalid enroll --master 10.0.0.1:7443 --token <one-time-token>
skalid agent
```

The join token is single-use, expires after an hour, and pins the cluster
CA's fingerprint, so the enrolling node authenticates the master before
trusting it. Steady-state node identity is a CA-signed cert (the node's UUID);
removing a node in the UI revokes it.

## Layout

```
api/           OpenAPI 3.1 contract (embedded, served by the daemon)
cmd/skalid     daemon: serve (default) | user | migrate | enroll | agent
cmd/skali      client CLI: auth, context
migrations/    goose migrations (embedded; also sqlc's schema source)
proto/         gRPC control-plane contract (buf; generated into internal/clusterpb)
query/         sqlc query sources → generated into internal/store
internal/
  api/         HTTP layer: router, middleware, error envelope, handlers
  auth/        auth service: argon2id, opaque sessions, TOTP 2FA, rate limits
  client/      typed REST client used by cmd/skali
  cliconfig/   ~/.config/skali/config.yaml contexts
  cluster/     node system: cluster CA, join tokens, enrollment, agent, poller
  clusterpb/   generated gRPC bindings (buf generate; checked in)
  config/      env-driven config (godotenv + envconfig)
  crypt/       shared at-rest encryption (AES-GCM, HKDF-derived keys)
  engine/      container engine adapter: Docker behind the Engine interface,
               label-driven ownership, container/inventory samplers, events
  hostinfo/    host resource sampler (CPU, memory, disk, network rates)
  mirror/      cluster image mirror: registry system container, docker trust
               distribution, digest-pinned imports + catalog
  obs/         slog + OpenTelemetry (env-only, zero egress by default)
  store/       pgx pool/tx glue + sqlc-generated queries
  testdb/      ephemeral Postgres database per test
web/           SvelteKit BFF (adapter-node)
```

## Roadmap

A living outline, roughest at the far end. Done so far: auth + users +
sessions (m1), the node system (m2), the container engine (m3), and the
registry mirror + event stream (m4) — the registry runs as a
`skali.kind=system` container serving cluster mTLS (node certs double as
docker client certs, installed at enrollment), the master imports upstream
images digest-pinned so workers pull from the LAN with no public egress,
and engine events ride a `WatchEvents` doorbell stream: a node resamples
and rings, the master resyncs it immediately, while the heartbeat stays the
level-triggered source of truth.

1. **Application layer** — projects, applications, releases: desired state
   on the master and a reconciler driving the node handles. Brings
   needed-set image GC (the master knows exactly which images each node
   needs; unneeded mirror-prefixed images are collected event-triggered
   with a periodic backstop), registry blob GC, and the self-managed
   control-plane Postgres boot path (engine first, then its own database
   container, then connect).
2. **Databases, volumes, routing** — shared/dedicated database pools with
   logical per-project databases, quota-enforced volume provisioning, and
   Traefik as the routed edge (per-node system components). Volume sizes
   via a slow-cadence disk-usage sweep may join here.

## Tests

```sh
# DB-backed tests create ephemeral databases on this server per test:
export TEST_DATABASE_URL=postgres://dev:dev@localhost:5432/dev?sslmode=disable
task test
cd web && npm run check
```
