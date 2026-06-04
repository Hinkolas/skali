# 17 — Resources, Bindings & Managed Data

This is the core model that makes skali a *platform* rather than just an app
deployer. The deploy chapters (`03`/`04`/`12`) describe the **application**
resource; this one defines the **resource model** that application is one type of,
and the two managed-data pillars — **databases** and **volumes** — that sit
alongside it. The ambition: a SvelteKit app + a Postgres database + a 10Gi uploads
volume, declared together and wired together, with no per-project database/backup
toil (the Sevalla idea, not just the Coolify one — see `00`).

## The model: typed Resources that Bind to each other

A **Project is a bundle** (`05`): a logical system composed of **typed
resources**. There are three types in 0.1.0:

| Type | What it is | Lifecycle | Stateful? |
|------|-----------|-----------|-----------|
| `application` | A built image run as containers behind the proxy | build → release → instance (`03`/`04`) | no — replicas are recreatable |
| `database` | A logical DB (Postgres) on a managed engine pool | provisioned once, backed up | **yes** — pinned, never recreated |
| `volume` | A quota-enforced disk mounted into a container | provisioned once | **yes** — pinned to a node |

`[future]` a fourth type — `bucket` (S3/object storage) — but **not as a runtime
resource**: external object stores are *connected as data* and surfaced in the UI
(like an external registry), not provisioned/mounted by skali. The 0.1.0 storage
pillar is **disk/volumes only**. (Decision: `16`.)

A **Binding** is the glue that wires resources together and generates the
ergonomics:

- **app ⟷ database** → skali injects a generated `DATABASE_URL` (and/or
  engine-specific vars) into the application's environment at release time. The
  app code just reads `process.env.DATABASE_URL`; it never sees provisioning.
- **app ⟷ volume** → the volume is mounted at the binding's `mount_path` in the
  container spec.

**N applications may bind one shared resource** (`bindings.target_id` is shared;
the schema is `05`). Four apps sharing one database = four binding rows, one
`databases` row.

## Allocation is decoupled from declaration

The single most important idea. The config declares a **requirement** — *"I need a
Postgres database called `main`"* — and says **nothing** about how it's physically
realized. **How** is an `allocation` placement policy, the exact analog of
application replicas + placement:

| `allocation` | Realization | When |
|--------------|-------------|------|
| `shared` *(default)* | A logical DB + isolated role on a **shared pool** that hosts many tenants | Small sites — "20 databases on one server." Cheap, dense. |
| `dedicated` | The database gets its **own pool** (engine container) | Needs isolation / its own resource budget. |
| `dedicated_host` | A dedicated pool placed on **its own node** | Needs maximum performance / blast-radius isolation. |

So scaling a database from "shares a box with 19 others" to "has its own server" is
**one knob**, not a migration to a different product concept. The declaration never
changes; only the policy does.

## Databases

### Pools and logical databases

A **DatabasePool** is a skalid-supervised engine container (Postgres in 0.1.0) —
managed exactly like Traefik and the registry (`10`): skalid creates it, owns its
lifecycle, and pins it to a node. A **Database** is a logical DB *inside* a pool
(`CREATE DATABASE` + a dedicated `CREATE ROLE`). A `shared` pool holds many
logical databases from many environments/projects; a `dedicated` allocation is
just a pool with one database in it. The pool's **data directory is itself a
quota-enforced volume** (below), so a pool can't run the disk out either.

### Connection injection (the `DATABASE_URL`)

When an application binds a database, the master:

1. ensures the logical DB + role exist on the assigned pool (idempotent),
2. derives the connection string from the pool endpoint + db name + role +
   generated password,
3. folds it into the application's **release env snapshot** (`05`) under the
   binding's `inject` name (default `DATABASE_URL`; configurable, and engine
   variants like `PGHOST`/`PGUSER` can be added).

The value is a normal env var from the app's perspective — so promotion/rollback
and the existing env-snapshot machinery (`03`) carry it for free, and **each
environment naturally gets its own DB** (prod binds prod's `main`, staging binds
staging's `main` — `05`).

### Isolation in a shared pool (security)

A shared pool is multi-tenant, so isolation is a security boundary (`08`):

- each logical DB gets its **own role**, **no superuser**, scoped so it can't see
  or connect to sibling databases (`REVOKE`/`GRANT` on the database, role can't
  enumerate others);
- the credential is generated per-database and stored server-side
  (`databases.secret_enc`);
- network reachability is **binding-gated**, not open (see connectivity below) —
  an app can reach a pool only because it bound a database on it.
- `[soon]` a connection pooler (pgbouncer-style) in front of shared pools, since
  many small apps × a few connections each can exhaust a Postgres connection
  budget.

### Backups (a first-class feature, not optional)

You named per-project backup toil as a Coolify pain, so managed databases imply
**skali-scheduled backups** (`backups` table, `05`):

- **0.1.0:** scheduled **logical** backups (`pg_dump`) per database on a
  retention policy, written to a **backup-target volume** (the Storage pillar
  backing the Database pillar — a clean synergy). Visible/triggerable via CLI/UI;
  restore is a guided operation.
- `[soon]` point-in-time recovery (WAL archiving / pgBackRest-style), off-node /
  off-site backup targets, restore-to-new-database.
- `[future]` engine major-version upgrades (`pg_upgrade`) — operationally heavy;
  the model leaves room but it isn't early scope.

## Volumes & the disk-safety floor

A **Volume** is a quota-enforced disk realized per environment and **pinned to a
node** (you can't move data by re-running a container). It is mounted into the
bound application at `mount_path`.

### The disk-safety floor (a real 0.1.0 invariant)

This directly answers the failure you called out — *"a runaway volume filled the
host disk and every app on the box died; this should never happen."* The floor:

1. **Every volume has an enforced `size_limit`.** A volume that hits its quota
   gets `ENOSPC` for *its own writes* — it cannot consume the rest of the disk.
2. **Volumes live on a data path isolated from host root and skali's control
   data**, and skalid **reserves headroom**: it refuses to provision (or grow) a
   volume that would breach the reserve. A full data partition can never take down
   `skalid`, the control DB, or sibling apps on other partitions.

So a runaway fills *its own quota* and that app degrades — blast radius of one,
not the whole node. The floor is committed for 0.1.0; the **enforcement
mechanism** is an implementation choice with tradeoffs (left open in `16`):

- **filesystem project quotas** (XFS/ZFS/btrfs) on the data dir — most robust,
  needs a quota-capable FS;
- **fixed-size loopback-backed volumes** — a hard cap on any FS, at some overhead;
- **usage monitoring + enforcement action** (alert, then set read-only) — a
  softer backstop, useful alongside either of the above.

Whatever the mechanism, the *invariant* (a volume can't exceed its quota; the host
keeps headroom) holds.

### Volume backends `[soon]`

0.1.0 uses node-local `local-quota` volumes. The `volumes.driver` column leaves
room for `[soon]` network/dedicated-storage-host backends (so a volume can outlive
a single node, enabling stateful rescheduling later) and `[future]` external
object storage surfaced as a connectable backend.

## East-west connectivity (binding-gated, not ingress)

App→database traffic is **east-west service connectivity**, fundamentally
different from the north-south ingress in `02`:

- It is **raw TCP** (the Postgres wire protocol), **not HTTP** — so it does **not**
  go through Traefik. Traefik is ingress/L7 only.
- The default network posture is **deny** (network-per-app isolation, `02`/`08`):
  apps can't see each other's containers, and that includes shared database pools.
- A **binding opens a deliberate path**: when app *X* binds database on pool *P*,
  skalid grants *X*'s network reachability to *P*'s listener (attach to a shared
  data network / open the pool port to *X*'s subnet) — and **only that**. An app
  with no binding to *P* still can't reach it.

So the shared-pool topology ("20 apps, one Postgres server") coexists with strict
default isolation: connectivity is **granted per binding**, never ambient.

## Stateful lifecycle (the reconciler carve-out)

The runtime invariant "**workers are stateless**" (`01`) holds for application
instances — they're recreatable replicas, blue/green-swapped and crash-restarted
freely (`03`). **Data resources are the deliberate opposite**, and the reconciler
treats them specially (`03`):

- **Pinned.** A volume/pool lives on a chosen node and is never auto-moved —
  moving it means migrating data, not re-running a container.
- **Provisioned once, idempotently.** The reconciler ensures existence + health;
  it **never destroys-to-recreate on drift**.
- **No blue/green.** You can't start a second Postgres on the same data and cut
  over; updates to a data resource are in-place and careful.
- **Never auto-rescheduled / never auto-pruned.** A dead node's databases/volumes
  are **not** recreated elsewhere (that would be silent data loss); they surface as
  degraded and wait for operator action. Removing a resource is always an explicit,
  gated operation (`--prune`/confirm, `11`).
- **Stateful rescheduling is `[future]`** and depends on a network/replicated
  volume backend (above) — deliberately out of 0.1.0.

## How it surfaces (pointers)

- **Config** (`11`): `databases:` / `volumes:` blocks declare requirements;
  `binds:` wires them to the app. Declarations resolve **by name across config
  shards** (convergent, conflict→error) so a polyrepo bundle works without a
  monorepo.
- **Schema** (`05`): `applications`, `database_pools`, `databases`, `volumes`,
  `bindings`, `backups`.
- **API** (`06`) / **CLI** (`09`): resource, database, volume, binding, and backup
  endpoints/commands; `skali db`, `skali volume`, `skali bind`.
- **Executor/gRPC** (`03`/`07`): provisioning operations (ensure volume with
  quota, provision pool, create logical DB + role, run backup) extend the same
  node-acting seam the application path uses.
- **Daemon** (`10`): skalid supervises database pools and manages volume quotas as
  owned infrastructure, the same pattern it uses for Traefik and the registry.
