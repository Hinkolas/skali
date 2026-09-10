# Building applications for skali

skali runs container applications as replicated, disposable processes
behind a managed edge, with PostgreSQL databases and S3 buckets
provisioned from the same manifest. Architecture that embraces this runs
well; architecture that assumes a single long-lived machine fights it.

## No durable local disk

The container filesystem is scratch space. `temporaryStorage` in
`resources:` is a cap on how much a replica may write, enforced by
eviction, and everything written there disappears when the replica is
replaced: on every deploy, on scaling, on node pressure, on crash.

Never treat the filesystem as storage:

- Uploads, generated files, artifacts, and anything users expect to see
  again belong in a bucket (`buckets:` in the manifest, S3 API in the
  app).
- Relational state belongs in a database (`databases:`).
- Caches may use the filesystem freely as long as a cold start with an
  empty cache is correct.

Persistent volumes exist (`volumes:` on an application) but they are the
escape hatch for third-party software that insists on a filesystem. A
volume forces the application to a single replica and `recreate`
rollouts, which means downtime on every deploy. Do not design new
applications around volumes.

## Replication safety

Assume several identical replicas run at once and any of them may serve
any request. Requests are not sticky.

- Sessions live in the database or in signed cookies, never in process
  memory.
- Background work that must run exactly once needs coordination through
  the database (locks, queues, unique job claims), not an "only one
  replica does this" assumption. If a component truly cannot be
  replicated, give it its own application with `scaling.replicas` left
  at 1 and keep the web tier scalable.
- In-process caches must be correct when stale, since each replica has
  its own.

## Configuration and secrets

All configuration reaches the application through environment variables
declared in the manifest. Three shapes appear in `environment:`:

- literals for values that are the same everywhere,
- `${NAME}` project values supplied per environment,
- `{{databases.k.output}}` and `{{buckets.k.output}}` service outputs
  the platform resolves at deployment.

Applications should read configuration only from the environment: no
config files baked into images, no environment detection. Credentials
for databases and buckets always come from service outputs; never
hand-write a connection string. Every project value is secret: stored
encrypted, write-only, and named but never printed in revisions, plans,
or logs.

## The lifecycle contract

skali drives deployments through signals the application must provide:

- Health endpoints. Declare `startup`, `readiness`, and `liveness` HTTP
  probes. Readiness gates traffic and rollout progress; a deployment is
  only active once replicas report healthy. A cheap `/healthz` that
  confirms the process can serve requests is the minimum; readiness
  should fail while the app cannot reach its dependencies.
- Migrations. `deployment.releaseCommand` runs once with the new image
  and resolved service outputs before the new version goes live. Put
  schema migrations there and keep them compatible with the release that
  is still serving: under the default `blue-green` the old version keeps
  serving until the switch, under `rolling` both versions serve at once.
  Expand in one release, contract in a later one.
- Shutdown. Replicas receive SIGTERM and have `shutdown.gracePeriod`
  (default 30s) to finish in-flight work. Handle SIGTERM: stop accepting
  work, drain, exit.

## Choosing a rollout strategy

Apply in order; the first match wins.

1. The application declares `volumes:`: `recreate`. Nothing else compiles.
   Every deploy is downtime, so keep such applications off the
   user-facing path.
2. The application runs many replicas (more than about ten) or the
   cluster cannot hold twice its replicas for a few minutes, and every
   release is compatible with the previous one (APIs, sessions, caches,
   schema): `rolling` with `maxUnavailable: 0` and the `maxSurge` the
   cluster can absorb.
3. Otherwise leave `rollout` out. The default `blue-green` fits web
   frontends, APIs, workers, and anything that serves content-hashed
   assets: the new version is fully ready before it takes any traffic,
   old and new never serve together, and a broken version never goes
   live. The price is double replicas during the deploy; size
   `resources.requests` so the cluster can hold them.

One gap remains under every strategy: browsers that loaded the previous
version may request assets only that version had. Keep the framework's
new-deployment detection and reload on (SvelteKit `version.pollInterval`,
Next.js build id checks); `blue-green` shrinks that window but cannot
close it.

## Scaling and placement

Declare capacity in the manifest, not in code:

- `scaling.replicas` with `min: 2` or more for anything user-facing, so a
  node failure or a replica restart does not cause downtime. Deploys are
  covered by the rollout strategy, not by replica count.
- `resources.requests` small enough that the cluster can run twice the
  replicas during a `blue-green` deploy; when it cannot, follow the
  rollout rule above.
- CPU autoscaling (`scaling.autoscaling.cpu.targetUtilization`) when
  load varies; it requires `max > min`.
- `placement.spread` with `enforcement: preferred` keeps replicas on
  different nodes in production while still allowing the same manifest
  to run on a single-node dev cluster.

Split roles into separate applications (web, worker, scheduler) sharing
databases and buckets through outputs, rather than one container that
does everything. Each application scales and rolls out independently.

## Local development parity

`skali dev` runs the same definition on a local cluster: same compiler,
same provisioning. Known boundaries: routes serve plain HTTP on
`*.localhost` domains (TLS is inert locally), databases share one
single-instance pool regardless of isolation and availability intents,
and bucket endpoints are in-cluster. Design for production and the dev
cluster follows.

An application may additionally declare a `dev:` block for a fast edit
loop: bare `skali dev` then skips building it and runs the dev command
(vite, `bun run dev`) on the developer's machine instead, while the
cluster's routes and sibling services are intercepted to reach the host
process. The process receives the application's real resolved
environment, with database and bucket addresses rewritten to loopback
ports the dev cluster publishes, so the managed Postgres and S3 are the
ones behind the hot reload. Two consequences to design for: the local
runtime (host node/bun) is not the container image, and the
`releaseCommand` does not run while intercepted; keep migrations and
seeds invokable as named `commands:` (`skali dev run migrate`) and use
`skali dev --preview` as the periodic full-parity check. Remote deploys
ignore both blocks entirely.

## Checklist

- Serves HTTP on one declared port, bound to 0.0.0.0.
- Health endpoints wired to `health:` probes.
- All state in `databases:` and `buckets:`; filesystem writes are
  disposable and fit inside `temporaryStorage`.
- Configuration read from environment variables only; credentials come
  from service outputs.
- Schema migrations in `deployment.releaseCommand`, backward compatible
  for one release.
- Rollout strategy chosen by the rule above: default `blue-green`,
  `rolling` only for many replicas with releases compatible with the
  previous one, `recreate` only with volumes.
- SIGTERM handled; in-flight work fits the grace period.
- At least two replicas for user-facing applications; no volume unless a
  dependency leaves no choice.

## A complete production-shaped manifest

```yaml manifest
version: "1"
name: team-wiki
description: Wiki with file attachments in a bucket and Postgres storage

applications:
  web:
    build:
      context: .
      dockerfile: Dockerfile
    environment:
      SESSION_SECRET: "${SESSION_SECRET}"
      DATABASE_URL: "{{databases.data.url}}"
      S3_ENDPOINT: "{{buckets.attachments.endpoint}}"
      S3_BUCKET: "{{buckets.attachments.name}}"
      S3_REGION: "{{buckets.attachments.region}}"
      S3_ACCESS_KEY: "{{buckets.attachments.access_key}}"
      S3_SECRET_KEY: "{{buckets.attachments.secret_key}}"
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
        tls: automatic
    health:
      startup:
        http:
          port: http
          path: /healthz
      readiness:
        http:
          port: http
          path: /health/ready
      liveness:
        http:
          port: http
          path: /healthz
    resources:
      requests:
        cpu: 0.2
        memory: 256MB
        temporaryStorage: 256MB
      limits:
        cpu: 1
        memory: 1GB
        temporaryStorage: 1GB
    scaling:
      replicas:
        min: 2
        max: 5
      autoscaling:
        cpu:
          targetUtilization: 70
    placement:
      spread:
        across: nodes
        minimum: 2
        enforcement: preferred
    deployment:
      releaseCommand:
        command: ["/app/wiki", "migrate", "up"]
        timeout: 5m
    shutdown:
      gracePeriod: 30s

databases:
  data:
    engine: postgres
    version: 17
    isolation: project
    storage:
      size: 20GB
    extensions: [citext]

buckets:
  attachments:
    visibility: private
    quotas:
      storage: 50GB

# Inactive declaration: scheduling and retention are not enforced.
backups:
  daily:
    schedule: "0 3 * * *"
    retention: 7d
    include:
      databases: all
      buckets: all
```

Backup policies are accepted but inactive: skali does not run scheduled backups or enforce retention. Create backups manually with `skali backup create`; inspect `skali backup --help` for restore and deletion commands.
