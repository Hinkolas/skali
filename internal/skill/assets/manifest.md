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
name: my-project    # required, matches ^[a-z][a-z0-9-]{0,62}$
description: ...    # optional free text
values: {}          # optional metadata for ${NAME} values
applications: {}
databases: {}
buckets: {}
backups: {}
```

At least one application, database, or bucket must be declared. Keys in
every collection are stable identifiers matching `^[a-z][a-z0-9-]{0,62}$`
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

The optional `values:` block adds metadata to `${NAME}` values:

```yaml
values:
  APP_DOMAIN:
    description: Public domain serving the application.
  SESSION_SECRET:
    secret: true
    description: Secret used to sign browser sessions.
```

Rules the compiler enforces:

- Secrecy comes only from the `values:` block. An environment file never
  decides what is secret.
- A secret value may only be referenced from an application's
  `environment:` block, and may not carry an inline default.
- A value declared in `values:` but never referenced anywhere is an
  error. Requiredness derives from use, not declaration.
- In `environment:` a reference must occupy the entire value; writing
  `"prefix-${X}"` there is an error. Route domains and build arguments
  allow concatenation of literals and plain values, but never secrets or
  service outputs.
- Malformed `${` or `{{` anywhere in a string is an error, so typos never
  pass through silently.

Values are supplied per environment: stored values are the default, and a
dotenv file can be staged at deploy (`--env-file`) or picked up
automatically from `./.env` by `skali dev`. An empty value in a dotenv
file counts as unset. `.env` and `.env.*` files never enter build
contexts.

## Applications

```yaml
applications:
  web:
    image: ghcr.io/acme/web:1.4.2   # exactly one of image or build
    build:
      context: ./web                # required with build; relative to project root
      dockerfile: deploy/Dockerfile # optional, relative to context, default Dockerfile
      target: runtime               # optional multi-stage target
      arguments:                    # optional build args; plain values only, never secrets
        VERSION: "${RELEASE:-dev}"
    command: ["/app/web", "serve"]  # optional container command
    environment:                    # names match ^[A-Za-z_][A-Za-z0-9_]*$
      NODE_ENV: production
      DATABASE_URL: "{{databases.data.url}}"
```

`image` references are imported into the managed registry and pinned by
digest. Build contexts may not be absolute paths and may not escape the
project root; `.skaliignore` in the context root filters files with
dockerignore syntax, and `.git/`, `.skali/`, `.env`, and `.env.*` are
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
outputs before replicas roll forward; it is the migration hook. Under
`rolling`, `maxUnavailable` and `maxSurge` cannot both be zero; under
`recreate`, setting either is an error. Replicas receive SIGTERM and have
the grace period to finish before being killed.

Volumes are the escape hatch, not the paved path: an application with
volumes is forced to `recreate` rollouts and a single replica. Prefer
databases and buckets; see `architecture.md`.

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
the only secret:

```yaml manifest
version: "1"
name: orders
description: Order API with a background billing worker

values:
  APP_DOMAIN:
    description: Public domain serving the API.
  STRIPE_KEY:
    secret: true
    description: Stripe API key used by the billing worker.

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
