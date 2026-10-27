# The skali.yaml manifest

This reference describes exactly one Skali release, named above it.
`skali skill read manifest`, run from a project directory, prints the
reference for the release of the project's target cluster.

Skali compiles one manifest at the project root into everything an
environment runs: applications, databases, buckets, and the values they
require. The same definition deploys unchanged to a local
`skali dev` cluster and to production.

## File and structure

- The file is `skali.yaml` or `skali.yml` at the project root. The CLI
  walks up from the working directory to find it; both names in the same
  directory is an error. `--manifest PATH` overrides discovery.
- Parsing is strict: unknown fields are rejected with file, line, column,
  and the field's path. A removed field names its replacement
  and the fix. Never invent fields; check this reference or run
  `skali validate` when unsure.
- Manifests contain no version or revision field. The local CLI records review
  history per manifest in untracked `.skali/manifest-review.yaml`. Missing history
  trusts current semantics and starts tracking after successful compilation.
  Relevant meaning/default changes since that revision require review; use
  `skali manifest upgrade --acknowledge` after reading the diagnostics.
  Ordinary validation never advances existing history. A newer local revision
  is retained when using an older target compiler; normal validation still runs.
  Fresh clones and server submissions have no historical review checks.
- `skali manifest upgrade` safely removes obsolete fields and updates local
  history after validation. No replacement version field is written. Unsupported
  YAML rewrite shapes require a manual edit. Local state never changes the
  compiled definition or its hash.
- Editors get completion and inline validation from the published schema
  by putting this on the first line:
  `# yaml-language-server: $schema=https://skali.dev/schemas/v1/skali.schema.json`
  This floating URL follows the newest **stable** release (the newest
  prerelease only when no stable release exists). It can therefore lack fields
  supported by a newer RC. Pin the editor to
  `https://skali.dev/schemas/<release>/skali.schema.json`, replacing `<release>`
  with the full tag, including `v`, from the reference header. The `v1` in the
  floating URL is neither a Skali release nor a manifest review revision.
  Editor validation supplements `skali validate`; it cannot check resolved
  hostnames, cluster capacity, live immutable settings, or deployment policy.

Top level:

```yaml
name: my-project    # required, matches ^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$
description: ...    # optional free text
applications: {}
databases: {}
buckets: {}
```

At least one application, database, or bucket must be declared. Keys in
service, port, route, volume, and named-command collections are stable identifiers
matching `^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`. Service keys are resource
identity: renaming a database or bucket replaces it and is destructive. Renaming
a volume creates an empty volume and retains the old claim until it is removed
manually or the environment is purged. Environment-variable names and build
argument names are not service keys.

The [field reference](#field-reference) below describes each key independently
of the examples. Use [values and expressions](#values-and-expressions) for
interpolation, and the topic sections for runtime behavior and restrictions.

## Values and expressions

Two expression forms appear inside string fields:

- `${NAME}` references a project value supplied per environment.
  `${NAME:-default}` supplies an inline default; without one the value is
  required before deployment. Names match `^[A-Z_][A-Z0-9_]*$`.
- `{{databases.<key>.<output>}}` and `{{buckets.<key>.<output>}}` are
  typed outputs of managed services, resolved by the platform at
  deployment. The referenced service must be declared in this manifest.

Available outputs:

| Collection | Plain | Secret |
| --- | --- | --- |
| `databases` | `host`, `port`, `name` | `username`, `password`, `url` |
| `buckets` | `endpoint`, `internal_endpoint`, `name`, `region` | `access_key`, `secret_key` |

`buckets.<key>.endpoint` is the bucket's route origin when it declares one
and the in-cluster gateway otherwise; `internal_endpoint` is always the
in-cluster gateway.

The value contract is derived entirely from `${NAME}` references; there
is no declaration block. Every value is secret: stored encrypted per
environment, write-only through the API and Studio, and shown in plans
and logs by name only.

Rules the compiler enforces:

- Requiredness derives from use: any `${NAME}` reference without an
  inline default makes the value required before deployment.
- Project-value expressions are supported in application `environment:`
  values, application route `domain` and each `aliases` entry, and bucket
  `route.domain`. Concatenation is allowed, such as `"www.${BASE_DOMAIN}"`.
  Hostnames are public, even when the values supplying them are stored as secrets.
- Other deployed string fields are literal: image, command, build target and
  arguments, route and probe paths, volume mount paths. A `${...}` there
  passes through verbatim, so shell syntax in `command` reaches the
  container untouched.
- Host-side `dev.command` has a separate expansion step against its resolved
  process environment; it does not declare project values. Named `commands`
  receive that environment but their argv elements stay literal; use an explicit
  shell to expand variables there. See [commands and local dev](#commands-and-local-dev).
- A `{{...}}` service output may appear only in an application's
  `environment:` block and must occupy the entire value.
- The same name with two different inline defaults is an error.
- Malformed `${` or `{{` inside an expression-bearing field is an error,
  so typos never pass through silently.

Values are supplied per environment. `skali deploy` and `skali dev` ask
once per environment which `.env`/`.env.*` file of the checkout to stage
(or to keep the stored values), remember the answer in
`.skali/env-files.yaml` (local, gitignored, no secrets), and show it in the
`values` header row from then on, with whether the file changed since the
last deployment; `--pick-env-file` asks again, `--env-file <path>` overrides
for one run, and a lone env file under `skali dev` is taken without asking.
`.env.example` and other templates are never offered. Keys the manifest
does not reference are skipped with a warning, never an error. An empty value in
a dotenv file is stored as a real empty string. Stored values the
manifest no longer references are ignored by deployments and reported as
orphaned; deploying with `--prune-values` (also on `skali dev`) removes
them as part of the plan. `skali values` and `skali dev values` list
what is stored by name and version. `.env` and `.env.*` files never
enter build contexts.

## Applications

```yaml
applications:
  web:
    image: ghcr.io/acme/web:1.4.2   # exactly one of image or build
    build:
      context: ./web                # required with build; relative to project root
      dockerfile: deploy/Dockerfile # optional, relative to context, default Dockerfile
      target: runtime               # optional multi-stage target, literal
      arguments:                    # optional literal build args; they persist in
        VERSION: "1.4.2"            # image config, so never put credentials here
    platforms: [linux/amd64]        # optional; platforms the image supports
    command: ["/app/web", "serve"]  # optional container command
    environment:                    # names match ^[A-Za-z_][A-Za-z0-9_]*$
      NODE_ENV: production
      DATABASE_URL: "{{databases.data.url}}"
```

`platforms` declares which platforms the application's image supports
(`linux/amd64`, `linux/arm64`); it applies to both `image` and `build`
sources. Leave it out for portable applications: they are built for
every platform the cluster's application nodes run. Declaring a subset
restricts builds to those platforms and pins the workload to matching
nodes. The candidates are intersected with the cluster's application-node
platforms; a configured cluster platform preference chooses one, otherwise
all candidates build. `--platform` explicitly overrides the build selection.

`image` references are imported into the managed registry and pinned by
digest. Build contexts may not be absolute paths and may not escape the
project root. The dockerfile resolves against the context (docker
convention) and may step outside it with `..`, but never outside the
project root; `.dockerignore` in the context root filters files exactly
as docker build would, and `.git/`, `.skali/`, `.env`, and `.env.*` are
always excluded.

### Ports and routes

```yaml
    ports:
      http:
        port: 8080          # required, 1 to 65535
        protocol: http      # http | https | tcp | udp, default http
    routes:
      public:
        domain: "${APP_DOMAIN}"   # required; ${} expressions allowed, service outputs not
        path: /                   # default /, must start with /
        port: http                # named port from this app, or a number
        tls: automatic            # automatic | optional | disabled, default automatic
        strategy: round-robin     # round-robin | least-requests, default round-robin
        compress: true            # true | false, default true
        aliases:                  # optional; hostnames redirecting to domain
          - "www.${APP_DOMAIN}"
```

A `udp` port is exposed over UDP and every other protocol over TCP.

Each route's domain and path pair must be unique across the whole
project; the same domain with different paths on different applications
is fine. `tls: automatic` provisions a certificate through the platform
edge in production and redirects plain HTTP to HTTPS; `tls: optional`
provisions the certificate but keeps answering plain HTTP without a
redirect (for consumers that cannot follow redirects); `tls: disabled`
serves plain HTTP only. Local development applies the same policies on
`*.localhost` domains, issuing from a development CA on ports 443 and 80,
so an application's origin is `https://${APP_DOMAIN}` locally as well;
`tls: disabled` stays `http://${APP_DOMAIN}`. On production, a
deploy waits for a route's certificate only when the domain already
reaches this installation. A domain still pointing elsewhere (a migration
in progress) is deferred: the run stays green, the checkpoint ends skipped
with a `TLS deferred` warning naming the domain, the ready summary prints
`cert deferred · domain not pointing here yet` plus a `warning:` line, the
Studio marks the run (`N routes deferred`, `succeeded · with warnings`)
and the service (`DNS pending`), and the certificate is issued
automatically once the A and AAAA records point here (one record left
behind reads as `partial` and still defers). Changing a route's domain on
an existing route (staging domain first, production domain later) defers
the same way: the certificate on hand was issued for the old domain and
keeps serving it (`issued for` in the checkpoint) while it counts as
unissued for the new one. When the domain arrives, the reconciler requests
a fresh issuance at once and records the arrival as a `reconcile` run; a
second run records the outcome (issued, or failed with cert-manager's
reason). `skali route probe` checks the domains right now, address by
address, and counts a domain that answers here as arrived, which also
retries a certificate stuck in backoff. A domain that does point here but
fails validation fails the run at the rollout deadline with the issuance
reason, for a first issuance and for a renamed route alike. Each route has an
**Issue TLS certificate** checkpoint with the
issuance attempt, consecutive failure count, last failure, estimated next retry,
and the current CertificateRequest, ACME Order and validation challenge details.
The CLI shows the checkpoint as one row per issuance attempt with its
failure reason or current wait and the retry countdown; `skali --verbose`
prints every recorded field instead. The Studio follows waiting
checkpoints live and shows a retry countdown. Retry times use the bundled
cert-manager backoff (1 hour, doubling up to 32 hours); controller scheduling
and CA rate limits can delay an attempt further.

After fixing DNS, redeploying requests a fresh issuance for a failed certificate
that has no usable certificate. This uses cert-manager's manual-renewal
transition once for a failure predating that promotion; repeated reconciliation
and daemon restarts do not bypass backoff again. A new failure whose next retry
falls after the rollout deadline fails the TLS checkpoint promptly. A first
deployment keeps converging after its run fails, so late issuance can still
activate it. A still-valid certificate with a failing renewal does not block a
deployment.

Check public A **and** AAAA records and inbound HTTP port 80 when validation
fails; a deferred checkpoint lists each resolved address and whether it
answered as this installation. HTTP routers reserve
`/.well-known/acme-challenge/` for cert-manager's solver and
`/.well-known/skali-edge` for the installation's own edge identity, and do
not redirect either to HTTPS. If the ACME response reports a rate
limit, honor the CA's retry window before redeploying; forcing another attempt
does not reset a CA limit.

`aliases` lists hostnames the edge answers with a permanent redirect to
the route's domain, keeping path and query (`www.example.com/a?b` lands on
`example.com/a?b`). Traefik answers 301 for GET and 308 for every
other method, so a form post keeps its method. The redirect covers the
alias's whole hostname whatever the route's path, never reaches the
application, and keeps working while the application is down or rolling
out. Each alias takes the route's `tls` policy: `automatic` gives it its own
certificate and sends plain HTTP straight to `https://<domain>`, `optional`
gives it a certificate and keeps the request's scheme, `disabled` redirects
plain HTTP only. Every alias certificate is a separate issuance with its own
**Issue TLS certificate** checkpoint (`… / <route> alias <hostname>`), so an
alias whose DNS still points elsewhere is deferred on its own (`skali route
probe` checks aliases too) and never holds back the route's certificate,
while an alias that does reach this installation gates the rollout like
the route's own certificate.
Like `domain`, an alias may use `${NAME}` expressions; no other field is
configurable. A hostname is either a route domain or an alias, project-wide:
it cannot also be a route's domain on any path, a bucket route, or another
route's alias.

Aliases are for alternate names of the same site: `www.`, a retired brand
domain. Browsers cache permanent redirects for a long time, so a hostname
you may want to serve something else on later should not be an alias. To
serve the application on a second hostname instead of redirecting, declare
a second route; serving one site under several hostnames splits cookies,
CORS origins, OAuth callbacks, and search ranking, so prefer an alias
unless both hostnames must answer. Path-level redirects (renamed pages)
stay in the application.

`strategy` selects how the edge balances requests across an application's
replicas: `round-robin` rotates evenly, `least-requests` sends each
request to the less busy of two randomly chosen replicas (better when
request durations vary, e.g. long streams next to fast calls). It only
matters above one replica.

`compress` controls edge response compression, on by default in production
and in local development alike: the edge answers with gzip, br, or zstd as
the client accepts for bodies of 1 KiB and more, skips event streams,
images, video, audio, fonts, archives, PDFs, and octet streams, and passes
through any response that already carries `Content-Encoding`. Applications
therefore need no compression of their own. `compress: false` turns it off
for one route; use it for routes that stream events under another content
type or that must deliver exact bytes.

### Health

```yaml
    health:
      startup:
        http:
          port: http        # named or numeric port
          path: /health/startup
        interval: 2s        # defaults: startup 2s, readiness 10s, liveness 30s
        timeout: 2s         # default 2s
        failureThreshold: 30 # defaults: startup 30, readiness 3, liveness 3
      readiness:
        http: { port: http, path: /health/ready }
      liveness:
        http: { port: http, path: /health/live }
```

Probes are HTTP only. They matter beyond restarts: a deployment is only
considered active once its replicas report healthy, so an application
without a readiness probe weakens the rollout signal.

### Resources

```yaml
    resources:
      requests:
        cpu: 0.2               # decimal cores, at most three decimals
        memory: 256MB
        temporaryStorage: 256MB
      limits:
        cpu: 1
        memory: 1GB
        temporaryStorage: 2GB
```

Requests reserve schedulable capacity; limits bound one replica. A
request may not exceed its matching limit. `temporaryStorage` caps the
writable container filesystem; exceeding the limit evicts the replica.
It is scratch space, not durable storage.

### Scaling and placement

```yaml
    scaling:
      replicas:
        min: 2         # default 1
        max: 5         # default = min
      autoscaling:
        cpu:
          targetUtilization: 70   # 1 to 100
    placement:
      spread:
        across: nodes            # nodes | zones
        minimum: 2
        enforcement: preferred   # preferred | required
```

Autoscaling requires `max > min`, and `max > min` requires a target
utilization; the two are declared together or not at all. `preferred`
spreading lets the same manifest run on a single-node dev cluster.

### Deployment, shutdown, volumes

```yaml
    deployment:
      releaseCommand:
        command: ["/app/web", "migrate", "up"]
        timeout: 5m
      rollout:
        strategy: blue-green # blue-green | rolling | recreate; default blue-green, recreate with volumes
        timeout: 10m         # wait for the new version to become ready; default 10m
    shutdown:
      gracePeriod: 30s       # default 30s
    volumes:
      state:
        mountPath: /var/lib/app   # required, absolute
        size: 5GB                 # required
```

The release command runs once with the new image and resolved service
outputs before the new version goes live; it is the migration hook. It
starts once the databases and buckets the application references accept
connections (waiting up to a minute), so it needs no connect-retry loop of
its own. Its `timeout` (default 10m) bounds the run; a failed or timed-out
release command fails the deployment before users see anything change.
It runs once per deployment: a failed command is never retried on its own,
only by the next deployment (`--force` redeploys the same revision). A
deployment cancelled or falling back while the command runs lets it finish
or reach its timeout instead of stopping it halfway.

`strategy` decides how the new version replaces the old one:

- `blue-green` (default): the new version starts next to the old one at
  full replica count and must become fully ready; then traffic switches
  to it in one step, the old version drains and is removed. Old and new
  replicas never serve at the same time, a version that never becomes
  ready never receives traffic, and rollback is an ordinary deploy of the
  previous revision. Costs twice the replicas for the duration of the
  deploy. Restarts take the same path.
- `rolling`: replicas are replaced a few at a time within
  `maxUnavailable` and `maxSurge` (default 1), which cannot both be zero.
  Both versions serve during the rollout, so every release must be
  compatible with the previous one: APIs, session and cache formats,
  schema. Choose it when the replica count is large or the cluster cannot
  hold a second full copy of the application.
- `recreate`: the old version stops before the new one starts; downtime on
  every deploy. Required and default for applications with `volumes:`;
  the other two strategies are rejected there.

```yaml
      rollout:
        strategy: rolling
        maxUnavailable: 0    # rolling only
        maxSurge: 1          # rolling only; default 1
```

`maxUnavailable` and `maxSurge` are errors under any other strategy.
Blue-green isolates routed traffic, not background execution: both versions'
processes exist during the overlap. Workers and scheduled jobs still need
database-backed coordination to avoid doing the same work twice.
`timeout` applies to every strategy: when the new version is not ready in
time the deployment fails and the previous revision stays active. The
clock starts at the deployment; a release command gets its own `timeout`
from when it starts, and the rollout `timeout` again after it. Traffic
that just switched to the new version gets one more check before a
deadline that passed moves it back.
Replicas receive SIGTERM and have the grace period to finish before being
killed.

Volumes are the escape hatch, not the paved path: an application with
volumes is forced to `recreate` rollouts and a single replica. Prefer
databases and buckets; read `skali skill read architecture` for this target.

### Commands and local dev

```yaml
    commands:                # named host-side commands (skali dev run)
      seed: [bun, run, db:seed]
      migrate: [bun, run, db:migrate]
    dev:                     # local dev server mode (skali dev)
      command: [bun, run, dev, --, --port, "${PORT}", --host]
```

Both blocks are client-only authoring surface: they never enter the
compiled definition, so editing them changes no deployment plan and rolls
nothing.

`commands` are named commands `skali dev run <name>` executes on this
machine in the project root, with the application's fully resolved
environment: stored values plus database and bucket outputs rewritten to
the local platform's loopback ports. Seeds and migrations against the dev
database live here. Keys are stable keys like every other name.

A `dev` block switches the application to local dev mode: bare `skali dev`
skips building it and runs `command` on the host instead, while the
cluster's routes and sibling services are intercepted to reach the host
process. Host ports are auto-allocated: every service port gets a
deterministic free port (range 20000-24999, override the base with
`SKALI_DEV_PORT_BASE`), stable per project, so concurrent projects never
collide. The chosen ports are injected into the command's environment as
`SKALI_PORT_<NAME>` (port name uppercased, hyphens to underscores) plus
plain `PORT` when the application has exactly one port, and `${VAR}`
expands inside `command` elements, so hand the port to tools that only
take a flag as shown above. The dev server must bind non-loopback
(`--host` for vite) because the cluster gateway connects from the
container network; the session warns when the port stays loopback-only or
silent. `dev.ports` (application port name -> host port) pins specific
ports instead; a pinned port that is already in use fails the session
rather than drifting. `skali dev --preview` ignores dev blocks and
deploys everything in the cluster; remote deploys always do. Note that an
intercepted application runs no release command, so run migrations through
`commands` while iterating locally.

## Databases

```yaml
databases:
  data:
    engine: postgres      # required; postgres is the only engine
    version: 17           # required; majors 17 and 18
    isolation: shared     # shared | project | dedicated, default shared
    availability: single  # single | asynchronous | synchronous, default single
    storage:
      size: 20GB
    extensions: [pg_trgm]
```

- `isolation`: `shared` packs onto the installation's shared pool per
  engine major, `project` gives the environment its own pool, `dedicated`
  gives this database its own pool.
- `availability`: `asynchronous` needs two database-capable nodes,
  `synchronous` needs three. Local development runs one single-instance
  pool and honors isolation logically.
- Extensions available on both majors: `btree_gin`, `btree_gist`,
  `citext`, `cube`, `earthdistance`, `fuzzystrmatch`, `hstore`,
  `intarray`, `ltree`, `pg_stat_statements`, `pg_trgm`, `pgcrypto`,
  `tablefunc`, `unaccent`, `uuid-ossp`, `vector`. `skali validate`
  rejects any other name and any version other than 17 or 18. `postgis`
  is not supported yet.
- `vector` is pgvector. Declare it here rather than in migrations:
  application roles are not superusers and cannot create it. A migration
  that runs `CREATE EXTENSION IF NOT EXISTS vector` stays a harmless
  no-op; a plain `CREATE EXTENSION vector` fails because it already
  exists.
- `recovery.pointInTime` (a duration such as `7d`) is valid vocabulary
  and is recorded with the database, but nothing acts on it yet: there is
  no point-in-time recovery. Restores come from backups.

Connect applications through outputs, never hand-written connection
strings:

```yaml
applications:
  web:
    environment:
      DATABASE_URL: "{{databases.data.url}}"
```

## Buckets

```yaml
buckets:
  files:
    visibility: private   # default; the v1 surface
    quotas:
      storage: 20GB
```

Buckets are S3 compatible. The v1 surface is private visibility with a
hard storage quota, an optional `cors` block (`allowedOrigins`,
`allowedMethods`, `allowedHeaders`, `exposeHeaders`, `maxAge`) for pages
that upload or download through presigned URLs, an optional `route`
block, and `lifecycle.abortIncompleteUploadsAfter` (a day when unset) for
abandoned multipart uploads; `visibility: public-read`, `versioning:
enabled`, `lifecycle.expireNoncurrentVersionsAfter`, `quotas.objects`,
and `quotas.maxObjectSize` are valid vocabulary already but are rejected
at deploy until later policies land. When a bucket reaches its quota it
refuses uploads until objects are deleted.

```yaml
buckets:
  files:
    cors:
      allowedOrigins: [https://app.example.com]  # required, at least one
      allowedMethods: [GET, PUT]   # GET | PUT | POST | DELETE | HEAD, default GET and PUT
      allowedHeaders: [content-type]
      exposeHeaders: [ETag]
      maxAge: 10m                  # how long browsers cache the preflight
```

Each origin is a literal scheme and host without a path, or `*`. Without a
`cors` block the store answers preflights for any origin; the presigned
signature, not the origin, authorizes a request. Declaring the block limits
which pages may talk to the bucket at all.

```yaml
buckets:
  files:
    route:
      domain: ${STORAGE_DOMAIN}   # project variable or literal hostname
      tls: automatic              # optional: automatic | optional | disabled
```

Without a route a bucket is reachable only from the environments that hold
it (its `endpoint` output equals `internal_endpoint`), like a database. A route
publishes the bucket on that hostname through the edge, path-style, with
a certificate and the same DNS probing, deferral and ownership checks as
an application route; `endpoint` becomes `https://<domain>` (`http://`
with `tls: disabled`), the address to sign presigned URLs for. The
hostname serves exactly this bucket. Several buckets may share a
hostname, each on its own path; a bucket route cannot share a hostname
with an application route. Changing or removing the route republishes
`endpoint` and rolls the consumers; URLs signed for the old host stop
working.

```yaml
applications:
  web:
    environment:
      S3_ENDPOINT: "{{buckets.files.endpoint}}"
      S3_BUCKET: "{{buckets.files.name}}"
      S3_REGION: "{{buckets.files.region}}"
      S3_ACCESS_KEY: "{{buckets.files.access_key}}"
      S3_SECRET_KEY: "{{buckets.files.secret_key}}"
```

## Backups

Backups are not a manifest concern; there is no `backups` key (a manifest
that still has one fails to parse and `skali manifest upgrade` drops the
block). Manual snapshots come from `skali backup create`. Automatic backups
are a per-environment setting an environment admin turns on with
`skali backup schedule set --environment <name> --every "0 3 * * *" --keep 7d`
(five-field cron evaluated in UTC, retention as a duration) or in the
environment's settings in the Studio; `skali backup schedule show` and
`skali backup schedule remove` read and turn it off. Every database,
bucket, and volume of the environment is snapshotted at each fire, and
snapshots the schedule took are deleted once older than the retention,
always keeping the newest one; manual snapshots never expire. A manifest
with no database, bucket, or volume has nothing to snapshot; backups of it
are refused.

## Units

- Byte quantities: decimal `B`, `KB`, `MB`, `GB`, `TB` (factor 1000) and
  binary `KiB`, `MiB`, `GiB`, `TiB` (factor 1024). Bare suffixes like
  `1G` are invalid; write `1GB` or `1GiB`.
- Durations: a number plus `ms`, `s`, `m`, `h`, `d` (24h), or `w` (7d).
- CPU: decimal cores with at most three decimals, such as `0.25` or `1`.

## Destructive changes

Removing a database or bucket from the manifest destroys its data. The
plan marks the change as destructive and deploy requires typed
confirmation; so does a rollback to a revision from before it was added.
Changing a live database's `engine`, `version`, or `isolation`, or a
live bucket's `visibility` or `versioning`, is refused outright; no
confirmation allows it. Quota changes update in place, but a bucket's
quota cannot drop to or below what it stores.

## Field reference

Each row is a manifest key, including object containers. `*` stands for a
user-chosen collection key, never a literal asterisk. Fields are optional unless
marked required. Omission defaults below describe effective behavior; some are
applied by the runtime rather than written into `skali compile` output. A
quantity is a positive byte value such as `256MB`; a duration uses the units
above. Arrays contain strings unless stated otherwise. An argv array executes
directly, without a shell; use `[sh, -c, "..."]` explicitly when a shell is needed.

### Project and application source keys

| Key | Type, default, and behavior |
| --- | --- |
| `name` | Required string; stable project key. Used to identify the project when there is no checkout binding. |
| `description` | String; empty by default. Human-readable project description, with no interpolation. |
| `applications` | Map of application keys to application objects; empty by default. At least one application, database, or bucket must exist. |
| `databases` | Map of database keys to managed database objects; empty by default. |
| `buckets` | Map of bucket keys to managed S3 bucket objects; empty by default. |
| `applications.*.image` | OCI image reference; exactly one of nonempty `image` or `build` is required. Imported into the managed registry and pinned by digest; literal, with no interpolation. |
| `applications.*.build` | Object describing a source build; mutually exclusive with `image`. Requires `context`. |
| `applications.*.build.context` | Required string when using `build`; path relative to the project root. `.` is valid; absolute paths and escaping the project are rejected. |
| `applications.*.build.dockerfile` | String; defaults to `Dockerfile` relative to the build context. May leave the context with `..`, but must stay inside the project root. |
| `applications.*.build.target` | String; unset by default, using the Dockerfile's final stage. Selects a named multi-stage build target. |
| `applications.*.build.arguments` | Map of build-argument names to string, number, or boolean scalars, converted to strings. Empty by default; passed literally, with no value or service-output expansion. Never put credentials here. |
| `applications.*.platforms` | Array; omitted or empty means no application restriction. Allowed values are `linux/amd64` and `linux/arm64`, without duplicates. Applies to both images and builds; order does not express preference. |
| `applications.*.command` | Argv array; omitted or empty preserves the image's command. Overrides the container command when set; no Skali interpolation. |
| `applications.*.environment` | Map of environment-variable names to string, number, or boolean scalars, converted to strings. Empty by default. Names match `^[A-Za-z_][A-Za-z0-9_]*$`. Supports project values and whole-value service outputs. Null, lists, and objects are invalid values. |

### Port and route keys

| Key | Type, default, and behavior |
| --- | --- |
| `applications.*.ports` | Map of stable port names to port objects; empty by default. Names are referenced by routes, probes, and dev port pins. |
| `applications.*.ports.*.port` | Required integer, 1–65535; port the container listens on. Declaring it does not publish it to the internet. |
| `applications.*.ports.*.protocol` | String: `http` (default), `https`, `tcp`, or `udp`. UDP uses UDP transport; the others use TCP. Routes publish HTTP traffic, not arbitrary public TCP/UDP listeners. |
| `applications.*.routes` | Map of stable route names to HTTP route objects; empty by default. No public application hostname exists unless a route is declared. |
| `applications.*.routes.*.domain` | Required nonempty hostname string; no scheme, path, port, or wildcard. Supports `${NAME}` and inline defaults; resolved names are checked and claimed at deploy. |
| `applications.*.routes.*.path` | String; default `/`. Literal path prefix beginning with `/`: `/api` also matches `/apix`. Dot segments, repeated slashes, and trailing slashes are cleaned before matching and duplicate checking. The prefix is preserved upstream. Queries, fragments, and control characters are invalid; the edge reserves its ACME and identity endpoints. |
| `applications.*.routes.*.port` | Required named port from this application or integer/numeric string in 1–65535; the upstream target, not the public edge port. |
| `applications.*.routes.*.tls` | String: `automatic` (default, certificate and HTTP-to-HTTPS redirect), `optional` (certificate and plain HTTP), or `disabled` (HTTP only). Also governs alias certificates and redirect schemes. |
| `applications.*.routes.*.strategy` | String: `round-robin` (default) or `least-requests`. Chooses how the edge distributes requests across replicas; unrelated to the rollout strategy. |
| `applications.*.routes.*.compress` | Boolean; default `true`. Enables edge compression for eligible responses. `false` disables it for this route; already encoded responses and standard event streams are skipped regardless. |
| `applications.*.routes.*.aliases` | Array of alternate hostnames; empty by default. Supports project-value expressions. Redirects the whole alias host to `domain`, preserving path/query (301 for GET, 308 otherwise); does not serve the app on a second hostname. Aliases must be unique and cannot also be any application or bucket route domain. |

### Health keys

| Key | Type, default, and behavior |
| --- | --- |
| `applications.*.health` | Object holding optional HTTP startup, readiness, and liveness checks. No probes are created when omitted. |
| `applications.*.health.startup` | Probe object; absent by default. Allows startup time before readiness and liveness checks begin; repeated failures restart the container. |
| `applications.*.health.startup.http` | HTTP check object, required for a declared startup probe. |
| `applications.*.health.startup.http.port` | Required named application port or numeric port in 1–65535. |
| `applications.*.health.startup.http.path` | Required literal HTTP path starting with `/`; no default. |
| `applications.*.health.startup.interval` | Duration; default `2s`. Time between startup checks. |
| `applications.*.health.startup.timeout` | Duration; default `2s`. Deadline for each startup check. |
| `applications.*.health.startup.failureThreshold` | Integer; default `30`. Consecutive failures before the container restarts. Use a positive value. |
| `applications.*.health.readiness` | Probe object; absent by default. Controls readiness for traffic and deployment activation; failure removes the replica from serving without restarting it. |
| `applications.*.health.readiness.http` | HTTP check object, required for a declared readiness probe. |
| `applications.*.health.readiness.http.port` | Required named application port or numeric port in 1–65535. |
| `applications.*.health.readiness.http.path` | Required literal HTTP path starting with `/`; no default. |
| `applications.*.health.readiness.interval` | Duration; default `10s`. Time between readiness checks. |
| `applications.*.health.readiness.timeout` | Duration; default `2s`. Deadline for each readiness check. |
| `applications.*.health.readiness.failureThreshold` | Integer; default `3`. Consecutive failures before the replica becomes unready. Use a positive value. |
| `applications.*.health.liveness` | Probe object; absent by default. Repeated failures restart an unhealthy container. |
| `applications.*.health.liveness.http` | HTTP check object, required for a declared liveness probe. |
| `applications.*.health.liveness.http.port` | Required named application port or numeric port in 1–65535. |
| `applications.*.health.liveness.http.path` | Required literal HTTP path starting with `/`; no default. |
| `applications.*.health.liveness.interval` | Duration; default `30s`. Time between liveness checks. |
| `applications.*.health.liveness.timeout` | Duration; default `2s`. Deadline for each liveness check. |
| `applications.*.health.liveness.failureThreshold` | Integer; default `3`. Consecutive failures before the container restarts. Use a positive value. |

### Resources, scaling, and placement keys

| Key | Type, default, and behavior |
| --- | --- |
| `applications.*.resources` | Object with per-replica requests and limits. Omitted fields impose no manifest-specific reservation or limit. |
| `applications.*.resources.requests` | Resource object; omitted by default. Reserves schedulable capacity per replica. Each request must not exceed its corresponding declared limit. |
| `applications.*.resources.requests.cpu` | Positive decimal CPU cores, as a number or string, with at most three decimals; unset by default. CPU autoscaling needs a CPU request to measure utilization. |
| `applications.*.resources.requests.memory` | Byte quantity; unset by default. Memory reserved for each replica. |
| `applications.*.resources.requests.temporaryStorage` | Byte quantity; unset by default. Ephemeral disk reserved for each replica. |
| `applications.*.resources.limits` | Resource object; omitted by default. Bounds the resources each replica may consume. |
| `applications.*.resources.limits.cpu` | Positive decimal CPU cores with at most three decimals; unset by default. CPU ceiling, enforced by throttling. |
| `applications.*.resources.limits.memory` | Byte quantity; unset by default. Memory ceiling; exceeding it can kill the container. |
| `applications.*.resources.limits.temporaryStorage` | Byte quantity; unset by default. Ephemeral storage ceiling; exceeding it evicts the replica. It does not provide persistent storage. |
| `applications.*.scaling` | Object; defaults to one fixed replica without autoscaling. |
| `applications.*.scaling.replicas` | Object setting the replica range. A fixed replica count uses `min` with `max` omitted or equal. |
| `applications.*.scaling.replicas.min` | Integer; default `1`. Minimum/fixed replica count; use at least 1. Volumes require one replica. |
| `applications.*.scaling.replicas.max` | Integer; default equals `min`, and must be at least `min`. A greater value requires CPU autoscaling. |
| `applications.*.scaling.autoscaling` | Object; absent by default. Defines automatic scaling policy; currently only CPU is supported. |
| `applications.*.scaling.autoscaling.cpu` | Object; absent by default. CPU utilization policy for the replica range. |
| `applications.*.scaling.autoscaling.cpu.targetUtilization` | Integer percentage, 1–100; no target by default. Required when `max > min`, and invalid with a fixed replica count. Measured relative to CPU requests. |
| `applications.*.placement` | Object; defaults to preferred spreading across nodes. |
| `applications.*.placement.spread` | Object controlling topology spreading with maximum skew 1. Preferred spread can still place replicas together when capacity is limited. |
| `applications.*.placement.spread.across` | String: `nodes` (effective default) or `zones`. Zones use the nodes' topology zone labels. |
| `applications.*.placement.spread.minimum` | Nonnegative integer; unset/0 by default. Minimum eligible topology domains; enforced only with `enforcement: required`. With preferred spreading it is recorded but does not constrain scheduling. |
| `applications.*.placement.spread.enforcement` | String: `preferred` (effective default) or `required`. Required spread leaves replicas pending when the constraint cannot be met. |

### Deployment and volume keys

| Key | Type, default, and behavior |
| --- | --- |
| `applications.*.deployment` | Object; omitted means no release hook and the default rollout policy. |
| `applications.*.deployment.releaseCommand` | Object; absent by default. Runs a one-off migration/hook with the candidate image and resolved environment before activation. Skipped for host-intercepted dev applications. |
| `applications.*.deployment.releaseCommand.command` | Argv array; empty/omitted means no release hook. Runs once per deployment without an automatic retry; no Skali interpolation. |
| `applications.*.deployment.releaseCommand.timeout` | Duration; effective default `10m`. Bounds the release hook once it starts; timeout fails the deployment. |
| `applications.*.deployment.rollout` | Object controlling replacement and readiness deadlines; valid with or without a release command. |
| `applications.*.deployment.rollout.strategy` | String: `blue-green` (default without volumes), `rolling`, or `recreate` (required and default with volumes). Blue-green needs capacity for both versions; recreate incurs downtime. |
| `applications.*.deployment.rollout.maxUnavailable` | Nonnegative integer; default `0` for rolling. Number of replicas allowed unavailable; valid only when strategy is explicitly/effectively rolling. Cannot be zero together with `maxSurge`. |
| `applications.*.deployment.rollout.maxSurge` | Nonnegative integer; default `1` for rolling. Extra replicas during rollout; rejected for blue-green and recreate, even when explicitly set to zero. |
| `applications.*.deployment.rollout.timeout` | Duration; effective default `10m`. Readiness deadline; a failed candidate does not become the active revision. The release command has its own timeout, followed by a fresh rollout deadline. |
| `applications.*.shutdown` | Object controlling container termination; optional. |
| `applications.*.shutdown.gracePeriod` | Duration; default `30s`. Time after SIGTERM to drain and exit before forced termination. |
| `applications.*.volumes` | Map of stable volume names to persistent volume objects; empty by default. Requires one replica and recreate rollouts. Removing a declaration retains its PVC; purging the environment deletes it. |
| `applications.*.volumes.*.mountPath` | Required absolute container path; literal. The volume is mounted read/write there. |
| `applications.*.volumes.*.size` | Required positive byte quantity. Requested persistent capacity; advisory on local-path storage. Local-path does not support resizing existing claims. Volumes can grow but cannot shrink. |

### Host development keys

| Key | Type, default, and behavior |
| --- | --- |
| `applications.*.commands` | Map of stable command names to nonempty argv arrays; empty by default. `skali dev run <name>` executes on the host in the project root with the application's resolved environment. Names shared by apps require an app-qualified selection. This map is client-only and does not change a deployment hash. |
| `applications.*.dev` | Object; absent by default. Enables host execution/interception during `skali dev`; requires `command`. Ignored by remote deploys and `dev --preview`, but still validated. Does not replace the required `image` or `build`. |
| `applications.*.dev.command` | Required nonempty argv array when `dev` is present. Runs the host dev server in the project root. `${VAR}` expands from the resolved process environment, including injected `SKALI_PORT_<NAME>` and `PORT` for a single port. Bind a non-loopback address. |
| `applications.*.dev.ports` | Map of declared application port names to integer host ports in 1–65535; empty by default. Pins only listed ports; other ports are allocated automatically. A busy pinned port fails instead of silently moving. |

### Database keys

| Key | Type, default, and behavior |
| --- | --- |
| `databases.*.engine` | Required string; only `postgres` is supported. Cannot be changed on an existing database. |
| `databases.*.version` | Required major number or string; supported values are `17` and `18`. Skali chooses the patch image. Changing a live database's major is refused; it is not a major-upgrade mechanism. |
| `databases.*.isolation` | String: `shared` (default, installation pool per major), `project` (pool for this environment), or `dedicated` (pool for this database). Immutable after creation. Dev uses one shared pool regardless. |
| `databases.*.availability` | String: `single` (default), `asynchronous` (two database-capable nodes), or `synchronous` (three). Declares minimum availability; insufficient capacity stays pending. Dev only provides single-instance availability. |
| `databases.*.storage` | Object with requested storage size; omitted by default. This is pool capacity intent, not a per-database usage quota. |
| `databases.*.storage.size` | Positive byte quantity; unset by default. Used when creating a dedicated pool, with a 10GiB managed-pool floor. Shared and project pools use platform capacity (10GiB initially); dev uses a 1GiB pool. Does not enforce an individual database quota or resize an existing pool. |
| `databases.*.extensions` | Array; empty by default. Supported names are listed in the Databases section, including `vector`; unknown names and duplicates fail validation. Applied before consumers become ready. Removing a name does not drop an installed extension. |
| `databases.*.recovery` | Object of recovery intent; absent by default. Currently recorded only, not an enabled recovery service. |
| `databases.*.recovery.pointInTime` | Duration; unset by default. Accepted and recorded, but point-in-time recovery is not implemented. Restore from snapshots instead. |

### Bucket keys

| Key | Type, default, and behavior |
| --- | --- |
| `buckets.*.visibility` | String: `private` (default) or `public-read`. Only private is deployable; public-read is recognized grammar but rejected at deploy. A public route does not change visibility or remove authentication. |
| `buckets.*.quotas` | Object; no quotas by default. Only storage quota is enforced in the current platform. |
| `buckets.*.quotas.storage` | Positive byte quantity; unset means no bucket storage quota. At/over quota, uploads are refused after the next upkeep measurement; reads/deletes continue. A new/lowered quota must exceed current usage. |
| `buckets.*.quotas.objects` | Nonnegative integer; unset/0 means no limit. Positive object-count limits are recognized but rejected at deploy. |
| `buckets.*.quotas.maxObjectSize` | Positive byte quantity; unset by default. Recognized but rejected at deploy. |
| `buckets.*.versioning` | String: `disabled` (default) or `enabled`. Enabled is recognized but rejected at deploy. |
| `buckets.*.lifecycle` | Object; optional. Abandoned multipart uploads are cleaned up even when it is omitted. |
| `buckets.*.lifecycle.abortIncompleteUploadsAfter` | Duration; effective default `1d`. Uploads older than this are aborted and their parts stop consuming quota. Applies to still-running multipart uploads too. |
| `buckets.*.lifecycle.expireNoncurrentVersionsAfter` | Duration; unset by default. Recognized but rejected at deploy because noncurrent-version expiry is not supported. |
| `buckets.*.cors` | Object; absent means the store's permissive preflight fallback (any origin, supported S3 methods, requested headers, exposed ETag). When declared, requires allowedOrigins and restricts browser access; signatures still authorize every S3 request. |
| `buckets.*.cors.allowedOrigins` | Required nonempty array when CORS is declared. Literal HTTP(S) scheme and host, optionally a port, without path/query/fragment; `*` permits every origin. No `${NAME}` expansion. |
| `buckets.*.cors.allowedMethods` | Array; defaults to `GET`, `PUT`. Allowed values: GET, PUT, POST, DELETE, HEAD; normalized to uppercase. OPTIONS preflights are handled by the store, not listed here. |
| `buckets.*.cors.allowedHeaders` | Array; empty by default in a declared policy. Headers browsers may send, e.g. `content-type`; use `*` when all request headers are needed. |
| `buckets.*.cors.exposeHeaders` | Array; empty by default in a declared policy. Response headers browser JavaScript may read; include `ETag` for multipart completion. |
| `buckets.*.cors.maxAge` | Duration; unset records zero (no positive preflight cache lifetime). Controls how long browsers may cache a preflight; converted to whole seconds. |
| `buckets.*.route` | Object; absent means in-cluster access only. Publishes this private bucket through the edge, using path-style S3 requests. No route path, aliases, compression, or strategy keys are supported here. |
| `buckets.*.route.domain` | Required hostname when route is declared. Supports project-value expressions. Becomes `endpoint`; `internal_endpoint` remains internal. May be shared by buckets in one environment, but not with application domains or aliases. |
| `buckets.*.route.tls` | String: `automatic` (default), `optional`, or `disabled`, with the same certificate/HTTP behavior as application routes. Endpoint uses HTTPS except when disabled. |

## Examples

### A minimal manifest

```yaml manifest
name: hello-world

applications:
  web:
    build:
      context: .
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
    health:
      readiness:
        http:
          port: http
          path: /healthz
      liveness:
        http:
          port: http
          path: /healthz
    scaling:
      replicas:
        min: 2
```

`${APP_DOMAIN}` is a per-environment value supplied at deploy time, so
the manifest carries no environment-specific data.

### Two applications sharing a database

The worker has no route and holds the Stripe key:

```yaml manifest
name: orders
description: Order API with a background billing worker

applications:
  api:
    build:
      context: .
    environment:
      DATABASE_URL: "{{databases.data.url}}"
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
    health:
      readiness:
        http:
          port: http
          path: /healthz
      liveness:
        http:
          port: http
          path: /healthz
    scaling:
      replicas:
        min: 2

  worker:
    build:
      context: .
    command: ["/app/orders", "worker"]
    environment:
      DATABASE_URL: "{{databases.data.url}}"
      STRIPE_KEY: "${STRIPE_KEY}"

databases:
  data:
    engine: postgres
    version: 18
    storage:
      size: 10GB
```

### A production-shaped manifest

Autoscaling, spread across nodes, a release command for migrations, and a
bucket for attachments:

```yaml manifest
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
```

Backups run once the installation has a backup target (`skali backup target set`, admin only). `skali backup schedule set` turns automatic backups on for one environment, `skali backup create` takes a snapshot by hand, `skali backup list` shows every snapshot with its origin (`manual` or `scheduled`), `skali backup restore` replays one, and `skali backup remove` deletes one. A restore that fails after stopping the environment keeps it down and refuses deploys until a restore completes or `skali backup accept` accepts the data as it is.

### The rest of the surface

Fields the examples above leave out: a bucket published on its own
hostname for browser uploads, a route alias, a rolling rollout, a worker
from a prebuilt image with a volume, and a host-side dev loop:

```yaml manifest
name: photo-share
description: Photo uploads through a bucket route, with a thumbnail worker

applications:
  web:
    build:
      context: ./web
      dockerfile: deploy/Dockerfile
      target: runtime
      arguments:
        VERSION: "2.0.0"
    platforms: [linux/amd64, linux/arm64]
    environment:
      DATABASE_URL: "{{databases.data.url}}"
      S3_ENDPOINT: "{{buckets.photos.endpoint}}"
      S3_BUCKET: "{{buckets.photos.name}}"
      S3_REGION: "{{buckets.photos.region}}"
      S3_ACCESS_KEY: "{{buckets.photos.access_key}}"
      S3_SECRET_KEY: "{{buckets.photos.secret_key}}"
    ports:
      http:
        port: 3000
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
        strategy: least-requests
        compress: true
        aliases:
          - "www.${APP_DOMAIN}"
    health:
      startup:
        http:
          port: http
          path: /healthz
        interval: 2s
        timeout: 2s
        failureThreshold: 60
      readiness:
        http:
          port: http
          path: /health/ready
        interval: 5s
        timeout: 1s
        failureThreshold: 3
      liveness:
        http:
          port: http
          path: /healthz
        interval: 30s
        timeout: 5s
        failureThreshold: 3
    scaling:
      replicas:
        min: 3
    deployment:
      rollout:
        strategy: rolling
        maxUnavailable: 0
        maxSurge: 1
        timeout: 15m
    commands:
      seed: [bun, run, db:seed]
    dev:
      command: [bun, run, dev, --, --port, "${PORT}", --host]
      ports:
        http: 5173

  thumbnailer:
    image: ghcr.io/acme/thumbnailer:1.2.0
    command: ["/thumbnailer", "watch"]
    environment:
      S3_ENDPOINT: "{{buckets.photos.internal_endpoint}}"
      S3_BUCKET: "{{buckets.photos.name}}"
      S3_ACCESS_KEY: "{{buckets.photos.access_key}}"
      S3_SECRET_KEY: "{{buckets.photos.secret_key}}"
    ports:
      metrics:
        port: 9100
        protocol: tcp
    volumes:
      cache:
        mountPath: /var/cache/thumbnailer
        size: 5GB
    deployment:
      rollout:
        strategy: recreate

databases:
  data:
    engine: postgres
    version: 18
    isolation: dedicated
    availability: asynchronous
    storage:
      size: 10GB
    extensions: [vector]

buckets:
  photos:
    visibility: private
    quotas:
      storage: 100GB
    lifecycle:
      abortIncompleteUploadsAfter: 2d
    cors:
      allowedOrigins: [https://photos.example.com]
      allowedMethods: [GET, PUT]
      allowedHeaders: [content-type]
      exposeHeaders: [ETag]
      maxAge: 10m
    route:
      domain: "${STORAGE_DOMAIN}"
      tls: automatic
```

The thumbnailer reads through `internal_endpoint`, which stays inside the
cluster, while `endpoint` is the bucket route's origin the web application
signs browser URLs for. `availability: asynchronous` needs two
database-capable nodes; local development runs it as a single instance.

<!-- BEGIN GENERATED MANIFEST CHANGES -->
## Manifest change history

This compiler understands manifest revision **10**. Revision numbers order
change records; they are not release versions, deployment revisions, or the
`v1` in the floating editor-schema URL. Never put a revision in the manifest.
Run `skali skill read manifest --since N` for entries strictly after N;
`--since 0` prints the full ledger. `skali manifest upgrade` records local
review after validating; `--acknowledge` confirms relevant semantic changes,
not permission to use unsupported fields or skip compilation.

The ledger began after the original grammar already existed, so it is not
an inventory of every feature. Revisions 7–10 backfill features that shipped
without records; they are informational additions, not changes introduced
by this release, and do not add acknowledgement gates. Old releases retain
their original embedded ledgers. The field reference above describes current
behavior; historical entries can describe fields that were later removed.

### Revision 1: removed `version`

version is no longer part of the manifest

run skali manifest upgrade to remove the obsolete version field

### Revision 2: added `backups.*.strategy`

backup policies gained an optional strategy field; complete is the only value and the default, and policies are now enforced: snapshots run on the schedule and retention deletes the ones they produced

backups has since been removed; configure automatic backups per environment with skali backup schedule set

### Revision 3: added `applications.*.routes.*.compress`

routes gained an optional compress field; the edge now compresses text-like responses by default (gzip, br, zstd) and compress: false opts a route out

nothing to change; write compress: false for routes that stream events or already compress their responses

### Revision 4: removed `backups`

backups was removed from the manifest; automatic backups are an environment setting now, one schedule per environment, set outside the manifest

run skali manifest upgrade to drop the block, then turn automatic backups on per environment with skali backup schedule set

### Revision 5: removed `skali`

the skali release watermark is no longer part of the manifest

run skali manifest upgrade to remove it; review history is stored locally in .skali/

### Revision 6: added `buckets.*.route`

buckets gained an optional route block (domain, tls); a bucket with a route is published on that hostname through the edge and its endpoint output becomes the https origin

nothing to change; declare route.domain on a bucket to publish it, buckets without one stay reachable in-cluster only (or on the installation-wide S3 endpoint while that still exists)

### Revision 7: added `applications.*.routes.*.aliases`

history backfill: application routes gained aliases, hostnames permanently redirected by the edge to the route domain while preserving path and query; aliases accept project-value expressions and follow the route's TLS policy

optional; use aliases for www or retired hostnames, and a separate route when the second hostname should serve the application; this records an already shipped feature without changing its behavior

### Revision 8: added `buckets.*.cors`

history backfill: buckets gained cors with allowedOrigins, allowedMethods, allowedHeaders, exposeHeaders, and maxAge; a declared policy replaces the store's permissive preflight fallback

optional; origins are literal HTTP(S) origins or *, methods default to GET and PUT, and browser multipart uploads need ETag exposed; this records already shipped support

### Revision 9: added `buckets.*.lifecycle.abortIncompleteUploadsAfter`

history backfill: deploy support was added for the existing abortIncompleteUploadsAfter vocabulary; the platform aborts multipart uploads older than this duration, with an effective default of one day when omitted

choose a duration longer than the longest expected upload; expireNoncurrentVersionsAfter remains unsupported; this informational entry records already shipped support and does not introduce a new cleanup default

### Revision 10: added `databases.*.extensions`

history backfill: supported extensions include vector (pgvector); the compiler checks extension names, duplicates, and PostgreSQL majors against the bundled catalog (currently majors 17 and 18)

declare extensions in the manifest before migrations need them; removing a name does not drop the installed extension; this records already shipped catalog support and validation
