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
  and the product database. It also carries platform updates: the
  coordinator hands each node an `upgrade` action naming a release, the
  agent downloads that release's `skali-hostd` from GitHub (verified against
  `checksums.txt`), swaps itself, restarts, and the new binary moves k3s to
  its own pin; the leader then converges the bundle to the release's images.
  `skalid` only writes the desired version into the cluster state (the
  console's Updates page, `internal/updates`); it never executes on a host.
  Both units read `/etc/skali/hostd.env`, where `SKALI_RELEASE_BASE` can
  point downloads at a mirror or a test server.
- **`web/`**: the console, a static SvelteKit SPA. Browser requests go
  directly to the Go API at `/api/v1/*`; Go owns the HttpOnly session cookie.
  `task build:web` builds and copies assets into `internal/webui/dist`, and
  `task build` embeds them in `skalid`. There is no production Node service.
  Vite proxies `/api` to the local daemon during development.

Auth is email and password (argon2id) with optional TOTP 2FA and backup
codes; sessions are opaque bearer tokens (sha256 at rest, 30-day sliding
expiry, instant revocation). There is no signup; users are created by an
admin. Access is one role ladder (`none < read < deploy < maintain <
admin`) per project and per environment; see
[`permissions.md`](permissions.md).

## Local setup

Requirements: Go 1.26.8+, Node 22+, [go-task](https://taskfile.dev), Docker
with Docker Compose (for the project Postgres, k3d dev cluster, and image
builds), and sqlc when changing queries.

```sh
# 1. Environment (DATABASE_URL, AUTH_SECRET, ...)
cp .env.example .env    # then set AUTH_SECRET: openssl rand -base64 32

# 2. Start this repository's Postgres 17 on 127.0.0.1:55432.
#    compose.yaml owns its container and persistent volume.
task db

# 3. Migrate and create the first admin
go run ./cmd/skalid migrate up
go run ./cmd/skalid user create --email you@example.com --role admin

# 4. Run the API on :7070
task dev

# 5. Point the CLI at it
go run ./cmd/skali remote add dev http://localhost:7070
go run ./cmd/skali remote status

# 6. The console (Vite on :5173, /api proxied to the local daemon)
(cd web && npm ci)
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

### Database migration baseline

`migrations/00001_baseline.sql` is the fresh-install schema for
`v0.1.0-alpha.1`. Future schema changes belong in new, sequentially numbered
Goose migrations starting at `00002`; never squash, rewrite, or renumber
released migrations. Preserve existing installation identities and data with
incremental migrations, and test upgrades against populated databases as well
as fresh installs. Regenerate the store with `sqlc generate` after schema edits.

Use `skalid migrate up` (or `go run ./cmd/skalid migrate up`) to apply the
embedded migrations and their baseline compatibility check. `migrate status`
performs the same check. Direct Goose commands bypass it; reserve them for
disposable development databases, including destructive Down testing.
Databases from the old, unpublished 30-migration history need a separate fresh
installation; see [prerelease safety](prerelease-safety.md) before replacing one.
Run `TEST_DATABASE_URL=... go test ./migrations` to exercise fresh installs,
repeat migration with existing data, baseline rollback/reapply, and refusal of
the old history.

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
# Start the project Postgres and run all Go tests, including DB-backed tests:
task test:db
(cd web && npm test && npm run check && npm run lint)

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
unset. `task test` also skips them unless you set `TEST_DATABASE_URL`; use
`task test:db` to start the project database and run them automatically.
Each test creates and drops its own database. `task db:stop` preserves the
development database; do not delete the Compose volume to fix a migration
error without first exporting any data you need. An external Postgres is
also supported: set `DATABASE_URL` and `TEST_DATABASE_URL` yourself.

## Building

```sh
task build          # bin/skalid, bin/skali, bin/skali-hostd (linux)
task install:cli    # the CLI into ~/.local/bin with the version stamped
task install:server # source install: build everything and run the installer
```

Releases are cut by tagging `v*`: goreleaser builds the binaries and
`install.sh`, and the workflow publishes the multi-architecture `skalid` image with its embedded console.
The bootstrap script defaults to `SKALI_CHANNEL=stable`; `SKALI_CHANNEL=beta`
includes alpha, beta, RC, and stable releases, selecting the highest version.
`SKALI_VERSION=vX.Y.Z` pins an exact tag and overrides channel selection.
It resolves the release once before fetching the CLI, host daemon, and checksums.
Tag with `task release:tag V=v0.1.0` rather than `git tag` by hand: it
validates the goreleaser config, refuses a dirty tree, a branch other than
main, a HEAD that is not origin/main, a malformed version, or a tag that
already exists, then confirms before pushing. Prerelease suffixes are
dotted `-alpha.N`, `-beta.N`, `-rc.N` only (the version package and the task
reject `-rc1`); such a tag becomes a GitHub prerelease that does not move the
`latest` image tags and is offered only to beta-channel clusters. Plain
`v0.y.z` tags are full releases; the leading zero is the initial-development
signal, not a prerelease.
The workflow also attaches `release.json` (`skali-schema --schema release`),
which names the k3s pin the release installs; the update scanner shows it
and the coordinator checks the k3s move against it before any host changes.
The daemon's daily scan reads the GitHub releases API (`SKALI_UPDATE_SCAN`,
`SKALI_UPDATE_FEED_URL` in `.env.example`). A scan that fails is recorded
with a kind (offline, not found, rate limited, unavailable, invalid) that the
Software update page turns into a notice, and is retried hourly until the
feed answers again; the last release found stays visible and updatable.
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
  updates/       release scan, update settings, and the cluster-state bridge
  valuestore/    versioned, encrypted, write-only environment values
web/           the console (SvelteKit)
```

Run `scripts/check-release-snapshot.sh` to rehearse a clean, nonpublishing release and verify its metadata against the built CLI.

### Static console builds

`task build` and the source Dockerfile build the console before compiling
`skalid`. GoReleaser does the same for releases and `task release:snapshot`
(including snapshots with Docker skipped), so these commands require Node 22.
CLI-only installation and ordinary Go tests do not require a frontend build.
A plain `go run ./cmd/skalid` without built assets keeps the API available and
returns 503 for console requests; use `task dev:web` for hot reload.

Set `SKALI_COOKIE_SECURE=false` for local HTTP development, as in `.env.example`.
Managed HTTPS installations explicitly enable secure cookies. The static
conversion targets fresh installs; recreate disposable clusters that used the
separate console deployment. New installations and subsequent upgrades only
need the daemon image; there are no web-image flags or `web` init-config block.
