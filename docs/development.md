# Developing skali

This page is for working on skali itself. Using skali is covered by the
[README](../README.md).

## Components

- **`skalid`** (`cmd/skalid`): the control plane. REST API plus the
  reconcile controller. Runs inside Kubernetes in production and carries the
  operator commands (`user`, `migrate`, `seed`, `backup-worker`).
- **`skali`** (`cmd/skali`): the CLI. Owns manifest, build, terminal, and
  local-development workflows and uses the public API for remote state
  changes. Its `skali cluster` group is the privileged host-level lifecycle
  (k3s, the installer-owned bundle); nothing else administers production
  Kubernetes.
- **`skali-hostd`** (`cmd/skali-hostd`): the installer-owned Linux host
  service on reconciled clusters. Every node runs the outbound-polling agent;
  servers also run the TLS enrollment coordinator. Independent of `skalid`
  and the product database.
- **`web/`**: the console, a SvelteKit BFF (adapter-node). Owns the browser
  session cookie and proxies `/_api/v1/*` to the daemon; the bearer token
  never reaches browser JavaScript. On a cluster it ships as the `skali-web`
  deployment: `/` serves the console and `/api` routes to the daemon, one
  surface over 80/443.

Auth is email and password (argon2id) with optional TOTP 2FA and backup
codes; sessions are opaque bearer tokens (sha256 at rest, 30-day sliding
expiry, instant revocation). There is no signup; users are created by an
admin. Access is one role ladder (`none < read < deploy < maintain <
admin`) per project and per environment; see
[`permissions.md`](permissions.md).

## Local setup

Requirements: Go 1.26+, Node 22+, [go-task](https://taskfile.dev), Docker
(for the k3d dev cluster and image builds), a Postgres for the control-plane
database, and sqlc when changing queries.

```sh
# 1. Environment (DATABASE_URL, AUTH_SECRET, ...)
cp .env.example .env    # then set AUTH_SECRET: openssl rand -base64 32

# 2. A Postgres reachable at DATABASE_URL with a database named "skali".
#    `task db` does this through a machine-global ~/Taskfile.yml that
#    provides a db:start task; without one, run any local Postgres and
#    point DATABASE_URL at it.
task db

# 3. Migrate and create the first admin
go run ./cmd/skalid migrate up
go run ./cmd/skalid user create --email you@example.com --role admin

# 4. Run the API on :7070
task dev

# 5. Point the CLI at it
go run ./cmd/skali remote add dev http://localhost:7070
go run ./cmd/skali remote status

# 6. The console (vite on :5173, BFF to the API)
cd web && cp .env.example .env && npm install
task dev:web
```

An API-only daemon has nothing to show until something is deployed. `task
seed` (`skalid seed`) fabricates a data-rich installation for working on the
console: seven users under `@seed.skali.local` (password `seed-password`,
`ada@` is an instance admin), five projects with several environments each,
memberships and environment cells, a week of deployment history (successes,
a failed rollout, a rollback, restarts, backups, one deployment still in
flight), provisioned database and bucket claims, and usage, edge, and
storage telemetry for the charts. `task seed:reset` removes exactly those
rows and seeds again; other data is left alone. Cluster observation (health,
pods, the node list) is not seeded and reads as unknown without a cluster.

Working from this repository, `skali dev` builds the `skalid:dev` image from
the working tree automatically (`task dev:image` refreshes it explicitly),
so the local platform always runs your code.

The OpenAPI contract lives in [`api/openapi.yaml`](../api/openapi.yaml) and
is served at `GET /openapi.yaml` (`/api/openapi.yaml` on a cluster); a
router-walk test keeps every registered route in the spec.

## Manifest tooling

skali discovers `skali.yml` or `skali.yaml` in the current directory or a
parent; `--manifest` selects one explicitly.

```sh
go run ./cmd/skali validate --manifest examples/hello-world/skali.yml

# The canonical, target-independent compiler IR.
go run ./cmd/skali compile --manifest examples/hello-world/skali.yml

# The Kubernetes objects, without a cluster. Image-sourced apps render
# as-is; build-sourced apps need their digest-pinned image via --image.
go run ./cmd/skali compile \
  --manifest examples/whoami/skali.yml \
  --target kubernetes \
  --env-file examples/whoami/.env.example
```

The editor schemas in [`schemas/`](../schemas/) are generated from the Go
types: run `task generate` (or `go generate ./internal/manifest`) after
changing manifest wire types, and keep `internal/skill/assets/` in step with
them; a test compiles every manifest fence in the skill.

## Tests

```sh
# DB-backed tests create an ephemeral database per test on this server:
export TEST_DATABASE_URL=postgres://dev:dev@localhost:5432/dev?sslmode=disable
task test
cd web && npm run check

# Live cluster tests (observation, apply/prune, healing) against a
# disposable pinned k3d cluster:
task k3d:up
task test:live
task k3d:down

# Build-engine tests exec docker (buildx plus a throwaway registry):
task test:docker

# The skali dev end-to-end suite drives the real paved path on its own
# throwaway installation (cluster skali-dev-e2e); it takes minutes:
task test:dev

# The cluster installer suite runs in Lima VMs:
task test:cluster
```

Bare `go test ./...` skips DB-backed tests when `TEST_DATABASE_URL` is
unset; use `task test` to be sure they ran.

## Building

```sh
task build          # bin/skalid, bin/skali, bin/skali-hostd (linux)
task install:cli    # the CLI into ~/.local/bin with the version stamped
task install:server # source install: build everything and run the installer
```

Releases are cut by tagging `v*`: goreleaser builds the binaries and
`install.sh`, and the workflow publishes the `skalid` and `skali-web` images.
Version pins for k3s, k3d, CNPG, cert-manager, Longhorn, and Lima live in
their packages next to their checksums; bump them together.

## Layout

```
api/           OpenAPI 3.1 contract (embedded, served by the daemon)
cmd/skalid     control plane: serve (default) | user | migrate | backup-worker
cmd/skali      CLI: dev, deploy, cluster, remote, and the rest
cmd/skali-hostd  host agent and enrollment coordinator for managed nodes
migrations/    goose migrations (embedded; also sqlc's schema source)
query/         sqlc query sources, generated into internal/store
schemas/       generated editor schemas (manifest, init, node, layout)
examples/      complete example projects
internal/
  api/           HTTP layer: router, access classes, handlers, SSE
  auth/ authz/   users, sessions, 2FA; the role ladder and route policy
  backup/        environment snapshots to an external S3 target
  build/         build engine: context hashing, buildx, digest imports
  bundle/        installer-owned skali-system bundle: render, apply, waits
  client/        typed REST client used by cmd/skali
  clusterstate/  enrollment coordinator and agent protocol (mTLS)
  compiler/      normalized project IR, references, dependency graph
  deploy/        deployment coordination: plan, artifacts, promotion
  hostdaemon/    skali-hostd agent loop
  installer/     host lifecycle: install, join, init, upgrade, diagnose, repair
  journal/       runs, steps, attempts, logs, SSE fan-out
  kube/          cluster client, server-side apply, field ownership
  kubernetes/    compiler IR to Kubernetes object rendering
  localdev/      the disposable local platform (k3d)
  manifest/      strict skali.yaml parser, diagnostics, schema generation
  metrics/       usage sampling: nodes, services, storage
  observe/       in-memory observed store fed by LIST/WATCH
  reconcile/     level-triggered kernel: apply, prune, health, activation
  registry/      managed registry client and token protocol
  skill/         the agent skill and `skali skill install`
  store/         pgx glue plus sqlc-generated queries
  substrate/     shared Postgres (CNPG) and S3 (SeaweedFS) provisioning
  valuestore/    versioned, encrypted, write-only environment values
web/           the console (SvelteKit)
```
