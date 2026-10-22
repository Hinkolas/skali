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
passes through definitions, revisions, or logs. The pool's port admits
only the environments holding a database on that pool: a pod in an
environment without one cannot open a connection, whatever credentials it
holds.

## Engines and versions

| Engine | Majors | Image |
| --- | --- | --- |
| `postgres` | 17, 18 | stock CloudNativePG `-system` images |

`skali validate` rejects a version outside this table.

### Patch updates

Each major runs one pinned image. A Skali release can pin a newer patch
release of a major, for example PostgreSQL 17.11 after 17.9. Pools created
from then on start on it, and existing pools move to it on their own once
the upgraded skalid runs, if the release lists the image the pool runs as
a reviewed source for the new one. Data stays where it is: a patch release
of the same major needs no dump and restore. The platform's own database
moves to its new pin while the upgrade converges.

Pools move one at a time, and only while every pool is healthy and running
its own image, so an update that does not come up stops at the first
pool. CloudNativePG restarts each pool in place: replicas first, then the
primary. A single-instance pool, including the local `skali dev` pool,
refuses connections for the seconds its restart takes; applications
should reconnect. A pool on an image the release has no reviewed
transition for keeps that image, and skalid logs
`pool keeps its image; no reviewed patch update from it`.

PostgreSQL patch releases sometimes recommend follow-ups that depend on
what a database holds, such as reindexing one index type after a fix to
it. Skali does not run them inside your databases. The notes of the Skali
release that ships an update list them; run the ones that apply with
`skali sql` or your own tooling.

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
  0.8.7 on both images at the time of writing). A patch update replaces
  the extension's library on every instance but does not run
  `ALTER EXTENSION ... UPDATE` in existing databases, so their recorded
  extension version stays as it was; running it remains a manual step.
  Each transition's review checks that the existing SQL definitions keep
  working with the new library.
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
availability stays pending. The pool's port still admits only the
environments holding a database, plus the host behind the loopback
NodePort. The pool comes up with the platform and stays running;
`skali dev stop` stops the whole platform with data retained.

## Changing a database after it exists

`availability`, `storage.size`, `extensions` and `recovery.pointInTime`
change in place on the next deploy. `engine`, `version` and `isolation`
cannot: they decide which pool holds the data, and Skali does not move a
database between pools. A plan preview, deploy, promotion or rollback
that would change one of them on an existing database is refused before
anything is built, naming the field and both values:

```text
databases.main.version cannot change from 17 to 18 on an existing database; its data cannot move in place.
Keep version 17, or declare a new database with version 18, copy the data across, and remove the old one
```

Confirming destructive changes does not get past this; the database, its
credentials and its data stay as they are. The check compares against
the database that exists, not the revision last deployed, so it also
holds after a rollout that failed or fell back.

To move to another version or isolation without losing data, declare a
second database under a new key with the new settings and deploy. Copy
the data into it with the tooling you already use for PostgreSQL (for
example `pg_dump` and `pg_restore` from an application or job that has
both connections), point the applications at the new database, and only
then remove the old one, which is a destructive change of its own.

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

Admins see and change all of this on the Studio's System > Databases
pages and with `skali database list`, `skali database show <name>` and
`skali database set <name> --memory 4Gi | --auto-memory --set key=value
--unset key`. Every pool has its own Studio page: the overview shows
the pool's usage over time (CPU and memory of its instances, client
connections, transactions, cache hit ratio and the size of its
databases), its instances with their roles and nodes, and the databases
on it; the tuning tab holds the budget and the parameters; the databases
tab lists every logical database with its owning project, environment
and measured size. The exporter figures are the primary's view: replica
reads are not observed. `skali database show <name>` prints the same
instances and databases.

## Connection health

A ready pool is not proof that applications can connect. Once the ordinary
connection slots are taken (`max_connections` less the slots PostgreSQL
keeps for superusers), the server refuses every new application login
with `53300` while its instances stay ready and connections that are
already open keep working. So skalid checks every pool about every 15
seconds with a fresh login, the way an application connects: as
`skali_probe`, an ordinary role with no privileges beyond counting
sessions (`pg_monitor`), run inside the primary instance over its local
socket. The check needs no password and no network path, holds one
connection for the length of one query, and keeps working while the
pool's other consumers are starved.

The check reads as one of four states:

| State | Meaning | Database health |
| --- | --- | --- |
| `available` | The login succeeded. | healthy |
| `saturated` | The login succeeded, and 90% or more of the connections applications may open are in use. | degraded |
| `refusing` | The server refused the login for lack of connection slots: applications cannot connect. | unhealthy |
| `unknown` | The check could not run (no ready primary, a timeout, the check's role not set up yet) or has not completed for 45 seconds. | unknown |

A state changes only after two checks in a row agree, so a pool that
starts or stops refusing shows within about 30 seconds (two checks, plus
up to 10 seconds for a slow one), and one slow check never flips health.
Every database on the pool takes the state into its health, and with it
every environment and project using one, with the server's reason in the
service's diagnostics. Members see that the pool refuses connections and
why, never other projects' usage. A refusing pool holds a rollout, since
the new revision could not connect either; `saturated` and `unknown` are
shown but never hold one, so a busy pool does not stall every deployment
on it.

A confirmed refusal opens an incident: an id, the refusal and its start,
kept in the platform database. It closes only after two checks in a row
logged in again, and survives a skalid restart, which neither forgets it
nor recovers it on a single login. Nothing has to be redeployed:
applications reconnect on their own, and health follows the next checks.
Admins see the state, the open incident, the recent incidents and each
database's connection count with `skali database show <pool>` and on the
pool's Studio page; `skali database list` and the pool list carry the
state. To recover, release connections (restart or fix the application
holding them, as the per-database counts show) or raise the pool's
`max_connections`, which restarts its instances.

The usage charts read the pool's metrics exporter. That exporter logs in
as an ordinary role too, so it loses access during the same exhaustion:
its gaps chart as gaps, never as zero connections.

## SQL access

`skali sql [database]` runs SQL against one database of an environment,
and `skali dev sql` against the local platform's, without a local psql and
without any application running. For each session skalid starts a Job in
the environment's namespace, the namespace the pool's access policy
already admits, with one container that idles until the session execs
into it: the pool's own pinned image (so psql matches the server's major
version) for the psql and csv formats, the skalid image's SQL runner for
`--format json`. The container's environment is the standard `PG*` set
taken from the database's connection Secret, the same mirror the
environment's applications consume; it mounts no other Secret and no
service account token, runs as nobody without privileges, and on a
managed cluster lands on an application node. The session streams over
the exec protocol (`GET .../databases/{key}/sql`), so authorization,
the promote-only sudo gate, the WebSocket edge path, and exit-status
passthrough are exec's.

The database's server pod is never a target. Several environments'
databases share a pool, and a shell there (psql's `\!` included) would
reach all of them; inside the client pod the same escape reaches only
what the session already has, this database's credentials. A batch
session's script travels as the session's stdin, never in the URL or the
command line, so SQL text is in no access log, process listing, or the
`sql session` audit line, which records the user, environment, database,
format, client pod, duration, and exit status.

The Job is deleted when the session ends. A daemon that goes away first
leaves it to its own bounds: an 8-hour active deadline (the longest a
session can run) and a TTL that removes it a minute after it ends. Clients
carry `skali.dev/sql-session` and none of the managed labels, so
observation, status, exec, and pruning never see them.

`--read-only` sets `default_transaction_read_only` and `--statement-timeout`
sets `statement_timeout`, both through `PGOPTIONS` at connect time. The
first is a guard against accidents, not a permission: the database's role
may `SET` it back.

## Credential rotation

`skali database rotate <key>` (or the database's "Rotate credentials"
button in the Studio) issues new credentials for one database without
touching its data. The rotation is a journaled run of kind `rotation` and
needs `maintain` on the environment and a recent login, like a reveal.

A PostgreSQL role holds one password and ownership cannot be shared, so
the overlap a rotation needs comes from two login roles under one owner.
The role a database is provisioned with is its owner: it owns the logical
database and everything the application creates in it. From the first
rotation on it never logs in again; login roles named after the credential
version (`<owner>_v2`, `<owner>_v3`, ...) alternate as its members, and
every session of a login role acts as the owner, so tables the application
creates still belong to the owner, migrations keep their privileges, and a
retired login role is dropped without leaving anything behind. The
`username` output changes with every rotation: read it from the outputs,
never pin it.

What happens, in order:

1. The pool creates the next login role with a new password and accepts
   both: the new one and the one it replaces.
2. The environment's outputs carry the new username and password and the
   credential version advances, so exactly the applications referencing
   the database restart with them (`blue-green` brings up the new color
   before the switch, `rolling` rolls). The run succeeds once every
   consumer runs only with the new credentials.
3. After the overlap window the previous login role is retired for good:
   its open sessions are terminated and the role is dropped (the owner
   role keeps its ownership and loses its login after the first rotation).
   The retirement happens on the platform's own clock, whether or not the
   run succeeded.

`--retire-after` sets the window (`1h` by default, `1m` at least, `7d` at
most). It must cover the consumer restart; `1m` is the setting for a
password known to be leaked: until the consumers have restarted, new
connections from pods still holding the old credentials may already be
refused. Rotating again inside the window retires the older login role at
once; there is never more than one previous login role.

Only pods started after the rotation hold the new credentials. Anything
that fetched them earlier must fetch them again: a `skali dev` host run
picks them up at its next start, and a copy taken through "Reveal
credentials" is simply stale. A backup or restore Job created before the
rotation and still running when the window ends loses its session; the
run fails and can be re-run.

Rotation and restore exclude each other through the environment's run
slot: one waits for the other. A restore runs as the current login role
acting as the owner, so it drops and recreates the database's objects
exactly as before. It runs in one transaction: a restore that fails
partway, on a damaged dump or a statement the database refuses, rolls
back and leaves the database as it was. The connection projection (the Studio's connection
panel, the connection API) shows the credential version and, inside a
window, the instant the previous login role retires.

Object storage rotates the same way, with a keypair instead of a login
role; see [buckets.md](buckets.md#credential-rotation).

## Deleting a database

Removing a database from the definition is a destructive change: the plan
marks it, deploy requires explicit confirmation, and teardown drops the
logical database, its credentials and its roles. Dedicated and project
pools are removed with their last database; the shared pool remains.
Teardown starts only once the revision that removes the database is
active: a rollout that fails and falls back finds it untouched. A
rollback to a revision from before the database was added removes it the
same way, and `skali rollback` asks for the same confirmation
(`--allow-destructive` when it runs unattended).

Object storage works the same way through the `buckets:` collection; see
[buckets.md](buckets.md).
