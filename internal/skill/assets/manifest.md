# The skali.yaml manifest

skali compiles one manifest at the project root into everything an
environment runs: applications, databases, buckets, backups, and the
values they require. The same definition deploys unchanged to a local
`skali dev` cluster and to production.

## File and structure

- The file is `skali.yaml` or `skali.yml` at the project root. The CLI
  walks up from the working directory to find it; both names in the same
  directory is an error. `--manifest PATH` overrides discovery.
- Parsing is strict: unknown fields are rejected with file, line, and
  column. Never invent fields; check this reference or run
  `skali validate` when unsure.
- Editors get completion and inline validation from the published schema
  by putting this on the first line:
  `# yaml-language-server: $schema=https://skali.dev/schemas/v1/skali.schema.json`

Top level:

```yaml
version: "1"        # required, always the string "1"
name: my-project    # required, matches ^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$
description: ...    # optional free text
applications: {}
databases: {}
buckets: {}
backups: {}
```

At least one application, database, or bucket must be declared. Keys in
every collection are stable identifiers matching `^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`
and are part of resource identity: renaming a key means destroying and
recreating what it names.

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
| `buckets` | `endpoint`, `name`, `region` | `access_key`, `secret_key` |

The value contract is derived entirely from `${NAME}` references; there
is no declaration block. Every value is secret: stored encrypted per
environment, write-only through the API and console, and shown in plans
and logs by name only.

Rules the compiler enforces:

- Requiredness derives from use: any `${NAME}` reference without an
  inline default makes the value required before deployment.
- Expressions live in exactly two places: an application's `environment:`
  values (`${NAME}` with concatenation such as
  `"postgres://app:${DB_PASSWORD}@db:5432/app"`) and a route's `domain:`
  (`${NAME}` with concatenation such as `"app.${BASE_DOMAIN}"`).
- Every other string field is literal: image, command, build target and
  arguments, route and probe paths, volume mount paths. A `${...}` there
  passes through verbatim, so shell syntax in `command` reaches the
  container untouched.
- A `{{...}}` service output may appear only in an application's
  `environment:` block and must occupy the entire value.
- The same name with two different inline defaults is an error.
- Malformed `${` or `{{` inside an expression-bearing field is an error,
  so typos never pass through silently.

Values are supplied per environment: stored values are the default, and a
dotenv file can be staged at deploy (`--env-file`) or picked up
automatically from `./.env` by `skali dev`. Keys the manifest does not
reference are skipped with a warning, never an error. An empty value in
a dotenv file is stored as a real empty string; removing a value is
explicit (`skali values unset NAME` or the console). Stored values the
manifest no longer references are ignored by deployments and reported as
orphaned. `.env` and `.env.*` files never enter build contexts.

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
    command: ["/app/web", "serve"]  # optional container command
    environment:                    # names match ^[A-Za-z_][A-Za-z0-9_]*$
      NODE_ENV: production
      DATABASE_URL: "{{databases.data.url}}"
```

`image` references are imported into the managed registry and pinned by
digest. Build contexts may not be absolute paths and may not escape the
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
        tls: automatic            # automatic | disabled, default automatic
```

Each route's domain and path pair must be unique across the whole
project; the same domain with different paths on different applications
is fine. `tls: automatic` provisions certificates through the platform
edge in production; local development serves routes over plain HTTP on
`*.localhost` domains.

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
        strategy: rolling    # rolling | recreate; default rolling
        maxUnavailable: 0
        maxSurge: 1          # default 1 under rolling
        timeout: 10m
    shutdown:
      gracePeriod: 30s       # default 30s
    volumes:
      state:
        mountPath: /var/lib/app   # required, absolute
        size: 5GB                 # required
```

The release command runs once with the new image and resolved service
outputs before replicas roll forward; it is the migration hook. Its
`timeout` (default 10m) bounds the run; a failed or timed-out release
command fails the deployment before any replica rolls. Under
`rolling`, `maxUnavailable` and `maxSurge` cannot both be zero; under
`recreate`, setting either is an error. Replicas receive SIGTERM and have
the grace period to finish before being killed.

Volumes are the escape hatch, not the paved path: an application with
volumes is forced to `recreate` rollouts and a single replica. Prefer
databases and buckets; see `architecture.md`.

### Commands and local dev

```yaml
    commands:                # named host-side commands (skali dev run)
      seed: [bun, run, db:seed]
      migrate: [bun, run, db:migrate]
    dev:                     # local dev server mode (skali dev)
      command: [bun, run, dev]
      ports:
        web: 5173            # application port name -> host port
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
process at the declared ports. `ports` maps the application's declared
port names to the host ports the dev server actually listens on; every
service port needs a mapping. `skali dev --preview` ignores dev blocks and
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
    recovery:
      pointInTime: 7d
```

- `isolation`: `shared` packs onto the installation's shared pool per
  engine major, `project` gives the environment its own pool, `dedicated`
  gives this database its own pool.
- `availability`: `asynchronous` needs two database-capable nodes,
  `synchronous` needs three. Local development runs one single-instance
  pool and honors isolation logically.
- Extensions available today: `btree_gin`, `btree_gist`, `citext`,
  `cube`, `earthdistance`, `fuzzystrmatch`, `hstore`, `intarray`,
  `ltree`, `pg_stat_statements`, `pg_trgm`, `pgcrypto`, `tablefunc`,
  `unaccent`, `uuid-ossp`. `pgvector` and `postgis` are not supported
  yet.

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
hard storage quota; `visibility: public-read`, `versioning: enabled`,
lifecycle rules, `quotas.objects`, and `quotas.maxObjectSize` are valid
vocabulary already but are rejected at deploy until later policies land.
When a bucket reaches its quota it turns read-only until space is freed.

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

```yaml
backups:
  daily:
    schedule: "0 3 * * *"   # required, five-field cron
    retention: 7d           # required duration
    include:
      databases: all        # all, or a list of database keys
      buckets: all
      volumes: all          # all, or application.volume references
```

At least one of the three include classes must be non-empty.

## Units

- Byte quantities: decimal `B`, `KB`, `MB`, `GB`, `TB` (factor 1000) and
  binary `KiB`, `MiB`, `GiB`, `TiB` (factor 1024). Bare suffixes like
  `1G` are invalid; write `1GB` or `1GiB`.
- Durations: a number plus `ms`, `s`, `m`, `h`, `d` (24h), or `w` (7d).
- CPU: decimal cores with at most three decimals, such as `0.25` or `1`.

## Destructive changes

Removing a database or bucket from the manifest destroys its data. The
plan marks the change as destructive and deploy requires typed
confirmation. Changing a live bucket's `visibility` or `versioning` is
also treated as a destructive replacement; quota-only changes update in
place.

## Complete example

Two applications sharing a database; the worker has no route and holds
the Stripe key:

```yaml manifest
version: "1"
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
