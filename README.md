# skali

Self-hostable hosting platform: declare an application or a managed database
in a project and skali runs it on your own hardware — Sevalla-style service
primitives instead of raw containers. skali is a **product control plane on
Kubernetes (k3s)**: user intent lives in Postgres, a controller compiles it
into k8s objects (server-side apply) and reads status back; k3s and a small
set of blessed operators (CloudNativePG, Traefik, cert-manager) do all
generic orchestration. The v2 architecture and rationale live in
[`REWORK_V2.md`](REWORK_V2.md).

**Status:** rearchitecture M0 — the hand-rolled substrate (custom node
system, Docker engine adapter, registry mirror, workload reconciler, gRPC
fabric) has been torn down; auth, the API framework, the web UI, and the CLI
remain. The service model (projects → applications/databases) is built next
on the k3s substrate. Roadmap: [`.plan/07-roadmap.md`](.plan/07-roadmap.md).

One API (`/v1`), three consumers:

- **`skalid`** — the control plane: REST API + (soon) the controller. One
  instance, in-cluster in production; nodes run only k3s. Also carries the
  operator commands (`user`, `migrate`).
- **`skali`** — the workflow-oriented CLI. It owns local manifest, build,
  terminal, and development-runtime workflows and uses the public API for
  remote state changes.
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

The dev Kubernetes substrate (k3d) and its task targets arrive with the
substrate half of M0 — see [`.plan/02-architecture.md`](.plan/02-architecture.md).

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
.plan/         the rearchitecture plan: substrate decision, architecture,
               service model, applications, databases, web/CLI, roadmap
```

Planned packages (see `.plan/02-architecture.md`): `internal/kube` (client,
SSA apply, informers), `internal/compile` (service → manifests),
`internal/services` (domain), `internal/controller` (reconcile loop).

## Roadmap

The full plan is [`.plan/07-roadmap.md`](.plan/07-roadmap.md); the short form:

- **M0 — demolition & substrate**: strip the custom orchestrator (done);
  k3d dev cluster, `internal/kube` plumbing, pinned bootstrap manifests.
- **M1 — projects & service core**: projects as namespaces, the typed
  services model, the controller loop, kube-backed read-only nodes page.
- **M2 — applications end to end**: image deploys as digest-pinned releases,
  Deployment/Service/Ingress compilation, rollbacks, logs, domains via
  Traefik.
- **M3 — databases on CloudNativePG**: shared pools + dedicated instances,
  logical database provisioning, credentials as k8s Secrets, app↔db
  connections with injected env.
- **M4 — polish**: TLS via cert-manager, analytics, CLI v1 (`skali deploy`),
  pool backups to S3.
- **Later**: user-facing backups, external DB access, compose import,
  templates, cache/object-storage/static-site types, environments with
  clone-from-prod, git builds.

History note: the pre-pivot custom orchestrator (m1–m5: node system, engine
adapter, registry mirror, workload reconciler, leader lease) was removed in
the M0 demolition; its history lives in git. The reasoning for the pivot is
recorded in [`.plan/01-decision-substrate.md`](.plan/01-decision-substrate.md).

## Tests

```sh
# DB-backed tests create ephemeral databases on this server per test:
export TEST_DATABASE_URL=postgres://dev:dev@localhost:5432/dev?sslmode=disable
task test
cd web && npm run check
```
