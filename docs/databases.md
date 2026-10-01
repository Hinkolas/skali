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

`skali validate` rejects a version outside this table.

## Extensions

Request extensions per database with `extensions: [pg_trgm, ...]`.
Available on both majors: `btree_gin`, `btree_gist`, `citext`, `cube`,
`earthdistance`, `fuzzystrmatch`, `hstore`, `intarray`, `ltree`,
`pg_stat_statements`, `pg_trgm`, `pgcrypto`, `tablefunc`, `unaccent`,
`uuid-ossp`, `vector`. `skali validate` rejects anything else, so an
unsupported extension fails before a deployment opens. `postgis` is not
supported yet.

`vector` is pgvector (the PostgreSQL extension name, not the project name):

```yaml
databases:
  data:
    engine: postgres
    version: 17
    extensions: [vector]
```

How extensions work on a pool:

- The stock pool images ship the extension files for every entry above, so
  they are present on every instance of every pool, replicas included, and
  on the local `skali dev` pool. No pool is restarted or rebuilt when a
  database requests one.
- Activation is per logical database. CloudNativePG runs `CREATE EXTENSION`
  for the listed names when it reconciles the database; a database on the
  same pool that does not list `vector` does not get it. A newly requested
  extension on a running database holds the application rollout until it
  exists, so a release command that creates a `vector` column or an
  `hnsw` index never runs ahead of it.
- Application roles are not superusers and pgvector is not a trusted
  extension, so migrations cannot create it themselves: declare it in the
  manifest. A migration that runs `CREATE EXTENSION IF NOT EXISTS vector`
  keeps working as a no-op; a plain `CREATE EXTENSION vector` fails because
  the extension already exists.
- The extension version is the one packaged in the pool's image (pgvector
  0.8.2 on the PostgreSQL 17 image and 0.8.6 on the 18 image at the time of
  writing). A pool image upgrade rolls the pool's instances but does not
  run `ALTER EXTENSION ... UPDATE` in existing databases; that remains a
  manual step.
- Removing an extension from the manifest stops declaring it and leaves the
  extension and every dependent column, index and row in place. Dropping it
  is a manual, destructive operation; Skali never cascades it.
- Vector indexes are built with the pool's `maintenance_work_mem` and share
  the pool's CPU and memory with every other database on it. A large
  embedding workload is a reason to choose `isolation: dedicated`.

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
pages and with `skali database list`, `skali database show <name>` and
`skali database set <name> --memory 4Gi | --auto-memory --set key=value
--unset key`. Every pool has its own console page: the overview shows
the pool's usage over time (CPU and memory of its instances, client
connections, transactions, cache hit ratio and the size of its
databases), its instances with their roles and nodes, and the databases
on it; the tuning tab holds the budget and the parameters; the databases
tab lists every logical database with its owning project, environment
and measured size. The exporter figures are the primary's view: replica
reads are not observed. `skali database show <name>` prints the same
instances and databases.

## Deleting a database

Removing a database from the definition is a destructive change: the plan
marks it, deploy requires explicit confirmation, and teardown drops the
logical database and its credentials. Dedicated and project pools are
removed with their last database; the shared pool remains.

Object storage works the same way through the `buckets:` collection; see
[buckets.md](buckets.md).
