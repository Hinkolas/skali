# Managed databases

Skali provisions PostgreSQL databases from the project definition. A
database is declared as a service and connected to applications through
typed output references; Skali places it on a managed pool, creates the
logical database and role, and injects the connection outputs.

```yaml
applications:
  web:
    environment:
      DATABASE_URL: "{{ databases.data.url }}"

databases:
  data:
    engine: postgres
    version: 17
```

Outputs: `host`, `port`, `name` (plain) and `username`, `password`, `url`
(secret). The application waits until the database is provisioned and
starts with the outputs injected; rotating or revealing credentials never
passes through definitions, revisions, or logs.

## Engines and versions

| Engine | Majors | Image |
| --- | --- | --- |
| `postgres` | 17, 18 | stock CloudNativePG `-system` images |

## Extensions

The stock images provide the PostgreSQL contrib set; request them per
database with `extensions: [pg_trgm, ...]`. Available today: `btree_gin`,
`btree_gist`, `citext`, `cube`, `earthdistance`, `fuzzystrmatch`,
`hstore`, `intarray`, `ltree`, `pg_stat_statements`, `pg_trgm`,
`pgcrypto`, `tablefunc`, `unaccent`, `uuid-ossp`. `pgvector` and
`postgis` are not yet supported; they arrive with a skali-built image.

## Isolation and availability

- `isolation`: `shared` (default) packs the database onto the
  installation's shared pool per engine major; `project` gives the
  environment its own pool; `dedicated` gives this database its own pool.
- `availability`: `single` (default), `asynchronous` (needs two
  database-capable nodes), `synchronous` (needs three). The shared pool's
  tier derives from the installation's database-capable node count; tier
  changes are explicit operations.

Local development (`skali dev`) runs exactly one single-instance pool:
isolation intents are honored logically but share it, and higher
availability stays pending. The pool comes up with the platform and stays
running; `skali dev stop` stops the whole platform with data retained.

## Pools and memory

Every pool runs a PostgreSQL parameter set derived from one number, its
memory budget. Stock PostgreSQL ships with 128 MB of `shared_buffers`,
sized for a laptop; a pool shared by several applications needs the node's
memory instead.

The budget is automatic unless an admin sets it. Automatic budgets are
sized from the smallest database-capable node: its allocatable memory minus
a reserve of 1 GiB or 10% (operating system, kubelet, the operator and the
platform's own database) is what pools may share; the shared pool takes
half of it, environment and dedicated pools a quarter each, handed out in
creation order so adding a pool never shrinks an existing one and the sum
never exceeds the node. Budgets are quantized to 128 MiB, and the local
development pool always runs a fixed 1 GiB. The budget also becomes the
instances' memory request (no limit), so the scheduler keeps that memory
for the pool.

Derived from the budget: `shared_buffers` (a quarter), `effective_cache_size`
(three quarters), `maintenance_work_mem`, `work_mem` (per backend, from
`max_connections`), plus SSD planner costs and WAL sizes from the volume.
Any of these, and a few more (`wal_buffers`, `wal_keep_size`,
`checkpoint_completion_target`, `default_statistics_target`, the parallel
worker limits), can be overridden per key in PostgreSQL syntax
(`work_mem=64MB`). Everything CloudNativePG manages itself (replication,
TLS, logging, `shared_preload_libraries`) is not tunable here.

Parameters that reload live take effect without interruption.
`shared_buffers`, `max_connections`, `wal_buffers` and
`max_worker_processes` need a restart: the operator restarts the replicas
first and then the primary, so a single-instance pool is briefly
unavailable, and a budget change moves `shared_buffers`.

Admins see and change all of this on the console's System > Databases
page and with `skali database list`, `skali database show <name>` and
`skali database set <name> --memory 4Gi | --auto-memory --set key=value
--unset key`.

## Deleting a database

Removing a database from the definition is a destructive change: the plan
marks it, deploy requires explicit confirmation, and teardown drops the
logical database and its credentials. Dedicated and project pools are
removed with their last database; the shared pool remains.

Object storage works the same way through the `buckets:` collection; see
[buckets.md](buckets.md).
