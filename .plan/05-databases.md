# 05 — Database services

The flagship primitive. A user declares "PostgreSQL 17, roughly this much storage" —
skali decides where it runs and what hosts it. Engine v1: **PostgreSQL via
CloudNativePG (CNPG)**. MySQL/MariaDB/Redis/Valkey appear in the picker as coming
soon; each later engine gets the same product shape behind the same tables
(engine-specific operator or the "simple tier" fallback: single instance + pinned
volume + scheduled dumps — same abstraction, honestly labeled guarantee level).

## Pools

The efficiency concept from the product vision: many logical databases share one
DBMS instance.

- A **pool** = one CNPG `Cluster` in `skali-dbs`, keyed by (engine, major version).
- **Packing rule (v1, deliberately dumb):** on database creation, reuse the shared
  pool for that (engine, major) if it exists, else create one. One shared pool per
  major version. Capacity-aware packing (spill to pool #2) only when reality demands
  it — measure first.
- **Dedicated pools:** a creation-time option ("dedicated instance") sets
  `dedicated_project_id`; the pool hosts only that service's database and its
  resources are the user's choice. This is the placement-control lever — not node
  picking. Node placement stays k8s's problem end to end.
- Pool sizing: blessed defaults per instance size (cpu/mem/storage on the
  `database_pools` row → CNPG resources + PVC size). Resize = update CR (CNPG
  handles volume expansion where the storage class allows).
- Pool Postgres version upgrades: CNPG minor updates are rolling and safe; major
  upgrades are a deliberate, user-visible operation — **later**, documented as such.

**Recorded tradeoffs of pooling** (why dedicated exists): noisy neighbors, shared
major-version lifecycle, physical backups are pool-granular, per-database "storage
limit" is a soft quota (Postgres has no native per-DB disk cap — we report
`pg_database_size` and warn at the soft limit). Dedicated pools have none of these;
Sevalla-style per-DB compute/storage maps to a dedicated pool.

## Blessed images & extensions

One blessed image per engine major (CNPG image catalog) that already contains the
extension set skali offers — v1: `pgvector`, `postgis` (the Sevalla-parity toggles).
Enabling an extension is per **logical database**, driven declaratively via the
Database CR / provisioning step. A pool image never needs to change because a tenant
toggled an extension.

## Provisioning a database service

On create, skali generates `db_name`, `db_user`, and a password (name/user
petname-style like the mock; DNS/SQL-safe), then compiles:

1. **Secret** (`skali-dbs` ns): the generated password. k8s Secrets are the only
   credential store (03). Password rotation = update Secret + CNPG reconciles the
   role + connected apps get a new release (04) — a v1 feature because the mock UI
   already has "Change password".
2. **Managed role** on the pool's CNPG Cluster (`.spec.managed.roles`): login role
   `db_user` with `passwordSecret` → the Secret above.
3. **CNPG `Database` CR**: logical database `db_name`, owner `db_user`, extensions
   list. Declarative create/reconcile/drop, no SQL scripting by skali.

Controller readiness: pool Cluster healthy + Database CR applied → service
`running`. A database service has `desired_state` running|stopped only in the
dedicated case (stop = scale Cluster to zero); shared-pool databases are always-on
(UI hides Stop) — pooling tradeoff, stated.

## Connection UX (mirrors the mock/Sevalla panels)

Internal connection (always available):

```
host      <pool-name>-rw.skali-dbs.svc.cluster.local
port      5432
database  <db_name>
user      <db_user>
password  read on demand from the Secret (reveal/copy in UI)
url       postgresql://user:pass@host:5432/db
```

`GET .../connection` assembles this; the BFF never caches the password. External
connections (from outside the cluster): **later** — the clean route is a TCP
entrypoint on Traefik or a NodePort per pool, plus IP allowlists; not v1.

Connected applications list = `connections` rows (04) — powers the "Connected apps"
panel and the connect flow in both directions (from the app or from the database).

## Backups & replication (the CNPG payoff — staged, not v1)

- **M4+ physical**: pool-level WAL archiving + scheduled base backups to S3-compatible
  object storage (CNPG `barmanObjectStore`) → PITR per pool. Needs an S3 target
  (external in v1; the future object-storage service type can host it later).
- **User-facing per-database backups** (the Backups tab): logical dumps (`pg_dump`)
  on schedule/manual to the same object store, restorable per database — correct
  granularity for pooled tenants; on dedicated pools PITR restore is also exposable.
- **Environments/clone-prod-to-staging**: CNPG recovery-from-backup / replica
  clusters gives "new pool from snapshot of prod pool"; skali maps logical DBs onto
  it. Designs cleanly on top of the above; build when environments land.

## Studio / Analytics tabs (mock parity)

- **Analytics** (M4): storage = `pg_database_size` (pooled) or PVC usage (dedicated);
  cpu/mem = pool-level from metrics-server, shown honestly as pool metrics for
  shared tenants.
- **Studio** (browser SQL console): later; needs its own authz story. Keep the tab
  stubbed as in the mock.
