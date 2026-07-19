# skali

Self-hostable hosting platform: declare an application or a managed database
in a project and skali runs it on your own hardware — Sevalla-style service
primitives instead of raw containers. skali is a **product control plane on
Kubernetes (k3s)**: user intent lives in Postgres, a controller compiles it
into k8s objects (server-side apply) and reads status back; k3s and a small
set of blessed operators (CloudNativePG, Traefik, cert-manager) do all
generic orchestration. The v2 architecture and rationale live in
[`REWORK_V2.md`](REWORK_V2.md).

**Status:** V2 rearchitecture — the hand-rolled substrate has been removed and
the strict manifest parser, editor schema, normalized compiler IR, dependency
graph, and deterministic Kubernetes renderer are in place. The current
architecture and roadmap are in [`REWORK_V2.md`](REWORK_V2.md).

Distinct product and operational roles:

- **`skalid`** — the control plane: REST API + (soon) the controller. It runs
  inside Kubernetes in production and also carries the operator commands
  (`user`, `migrate`).
- **`skali`** — the workflow-oriented CLI. It owns local manifest, build,
  terminal, and development-runtime workflows and uses the public API for
  remote state changes; it does not administer production Kubernetes.
- **`skali-installer`** *(planned)* — the privileged, repeatable installation
  and recovery tool for host-level k3s lifecycle and installer-owned Skali
  system resources. It is not a continuously running host daemon.
- **`web/`** — SvelteKit BFF (adapter-node). Owns the browser session cookie
  and proxies `/api/v1/*` to the daemon; the bearer token never reaches
  browser JavaScript.

Auth is email+password (argon2id) with optional TOTP 2FA and backup codes;
sessions are opaque bearer tokens (sha256-hashed at rest, 30-day sliding
expiry, instant revocation). There is no signup endpoint — users are created
by the operator.

## Quickstart (dev)

Requirements: Go 1.26+, Node 22+, [go-task](https://taskfile.dev), Docker
(shared dev Postgres; k3d dev cluster), sqlc (only when changing queries).

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

The planned local Kubernetes workflow and parity boundaries are defined in the
[V2 local-development section](REWORK_V2.md#11-local-development-and-cli).

The OpenAPI contract is served at `GET /openapi.yaml` and lives in
[`api/openapi.yaml`](api/openapi.yaml); a router-walk test keeps it honest.

## Manifest compiler preview

Skali discovers `skali.yml` or `skali.yaml` in the current directory or a
parent. An alternative complete definition can be selected explicitly with
`--manifest`.

```sh
go run ./cmd/skali validate --manifest examples/hello-world/skali.yml

# Inspect the canonical, target-independent compiler IR.
go run ./cmd/skali compile --manifest examples/hello-world/skali.yml

# Preview the deterministic Kubernetes objects without touching a cluster.
go run ./cmd/skali compile \
  --manifest examples/hello-world/skali.yml \
  --target kubernetes \
  --env-file examples/hello-world/.env.example
```

The generated editor schema is checked in at
[`schemas/skali.schema.json`](schemas/skali.schema.json). Run
`go generate ./internal/manifest` after changing the manifest wire types.

## Layout

```
api/           OpenAPI 3.1 contract (embedded, served by the daemon)
cmd/skalid     control plane: serve (default) | user | migrate
cmd/skali      workflow CLI: auth, context, validate, compile
migrations/    goose migrations (embedded; also sqlc's schema source)
query/         sqlc query sources → generated into internal/store
internal/
  api/         HTTP layer: router, middleware, error envelope, handlers
  auth/        auth service: argon2id, opaque sessions, TOTP 2FA, rate limits
  client/      typed REST client used by cmd/skali
  compiler/    normalized project IR, references, units, dependency graph
  cliconfig/   ~/.config/skali/config.yaml contexts
  config/      env-driven config (godotenv + envconfig)
  crypt/       shared at-rest encryption (AES-GCM, HKDF-derived keys)
  kubernetes/  pure compiler IR → Kubernetes API object rendering
  manifest/    strict skali.yml parser, diagnostics, and schema generation
  obs/         slog + OpenTelemetry (env-only, zero egress by default)
  store/       pgx pool/tx glue + sqlc-generated queries
  testdb/      ephemeral Postgres database per test
web/           SvelteKit BFF (adapter-node); lib/mock is the services design
               spec being promoted to the real API milestone by milestone
.plan/         superseded first Kubernetes rework plan; historical context only
```

Current package boundaries and planned subsystems are defined in
[`REWORK_V2.md`](REWORK_V2.md); `.plan/` must not be used as the V2
implementation contract.

## Roadmap

The authoritative milestones and exit criteria are in
[`REWORK_V2.md`](REWORK_V2.md). The short form is:

- **R0 — architecture contract:** schemas, fixtures, ownership, artifacts, and
  installer/local-runtime contracts.
- **R1 — domain and persistence:** immutable revisions, environment values,
  artifacts, run journals, and service-module contracts.
- **R2 — observation and reconciliation:** LIST/WATCH state, health, generic
  apply/prune, resync, and healing.
- **R3 — local application slice:** in-cluster local Skali, `skali dev`, builds,
  deploys, logs, and application health.
- **R4 — installer and remote delivery:** K3s/bootstrap maintenance, production
  registry, remote deployment, and cloud builds.
- **R5/R6 — managed data services:** shared database substrate followed by
  object storage and bucket services.
- **R7/R8 — product completion:** web UI, durability, recovery, compatibility,
  and release hardening.

History note: `.plan/` records the first Kubernetes rearchitecture that preceded
V2. It is retained for historical rationale—especially the substrate decision—
but its schemas and M0–M4 roadmap are superseded. The older custom orchestrator
also remains available through git history.

## Tests

```sh
# DB-backed tests create ephemeral databases on this server per test:
export TEST_DATABASE_URL=postgres://dev:dev@localhost:5432/dev?sslmode=disable
task test
cd web && npm run check
```
