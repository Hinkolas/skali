# 16 — Open Questions & Risks

Decisions we have **not** made, and design tensions still live. **Resolved
decisions moved to `DECISIONS.md`** — this file holds only what's open, so it stays
short. Each item notes a recommendation so we have a default.

## New & open from the resource/data work (`17`)

> **Volume quota enforcement** is now **decided** → see `DECISIONS.md` ("Volume
> quota enforcement = XFS project quotas on a skali-managed filesystem") and `17`.

### A. Pool placement & scheduling policy
Where does a new **shared** pool land, and when do we spin up another vs. pack more
databases onto an existing one? **Open.** **Recommendation:** 0.1.0 = one shared
pool per engine on the (single) node, simple "pack until a configurable per-pool DB
cap, then create another"; richer bin-packing / load-aware placement `[soon]`.
Dedicated/`dedicated_host` allocations are explicit and bypass this.

### B. Connection pooling for shared pools
Many small apps × a few connections each can exhaust a Postgres connection budget
on a shared pool. **Open: do we need pgbouncer in 0.1.0 or `[soon]`?**
**Recommendation:** ship 0.1.0 with sane per-database connection caps; add a
pgbouncer-style pooler in front of shared pools in `0.2.x` (`15`). *Watch this if
early bundles are connection-heavy.*

### C. Backup target for 0.1.0
Scheduled `pg_dump` has to write *somewhere*. **Open:** a local backup-target
volume only (simple, but same-node ⇒ weak DR) vs. requiring an off-node target.
**Recommendation:** 0.1.0 = a dedicated local backup volume (better than nothing,
honest about its DR limits); off-node / off-site targets `[soon]` (`15`). Document
that same-node backups don't survive node loss.

### D. Cross-shard convergence: when is the picture "complete"?
In a polyrepo bundle (`11`), the master only sees a shard's declarations when that
shard `apply`s. So a `binds: [database: main]` reference can be "dangling" simply
because the *defining* shard hasn't applied **yet**, not because it's wrong.
**Open: how strict to be, and when.** **Recommendation:** error on a dangling
reference **at apply time for that shard** (deterministic, per `DECISIONS.md`), but
make the message distinguish "no shard defines `main`" from "defined elsewhere, not
yet applied," and let `skali config validate` (`[soon]`) check the known
cross-shard picture. *Revisit if ordering between repo deploys becomes painful (CI
may need a defined apply order, or a "define-first" convention).*

### E. Database engine version upgrades
Major-version upgrades (`pg_upgrade`) of a pool are operationally heavy and risk
downtime/data. **Decided out of 0.1.0** (`15` `[future]`); **open** how we do them
safely later (in-place vs. dump-restore-into-new-pool vs. logical replication
cutover). **Recommendation:** pin a version per pool in 0.1.0, make upgrades an
explicit, backed-up, opt-in operation when we build them.

## Carried-over / still genuinely open

### 1. Master is a single point of failure
SQLite-on-master means no control-plane HA. Data plane survives a master outage
(running apps keep serving; Traefik caches config; **databases keep serving** —
they're independent containers), but no changes can be made and config goes stale.
**Recommendation:** accept for 0.1.0; document it; plan litestream backups now as
the stepping stone, real HA `[future]`. *Decide how much this bothers you.*

### 2. gRPC connection direction under NAT
Plan assumes **master dials worker** (fine for "mutually reachable private
network"). A worker behind NAT that can't accept inbound would need to **dial out**
and hold a reverse stream. **Recommendation:** keep master-dials-worker for 0.1.0;
design the gRPC layer so a reverse-dial transport can be added without changing the
`Executor`/service layers. *Do any target topologies have un-routable workers?*

### 3. Build resource isolation / multi-tenant builds
BuildKit builds run on a `builder` node with Docker-daemon access — effectively
privileged. Fine for single-operator 0.1.0; for untrusted multi-tenant use
`[future]` consider rootless BuildKit / isolated build sandboxes.

### 4. Per-environment deploy concurrency
Two deploys racing the reconciler. **Plan: per-environment lock** (`05`) — same
Environment serializes, different environments deploy concurrently. **Open:** queue
vs reject-second. **Recommendation:** serialize (queue) per environment; reject only
if one is already queued+running and `--no-wait`.

### 5. Log persistence
0.1.0 streams logs on demand from Docker (no storage) — logs vanish with the
container. **Open:** persisted/searchable logs? **Recommendation:** on-demand for
0.1.0; pluggable log shipping `[future]`.

### 6. Naming: "master"
We use `master` for the control-plane role. Some projects prefer
`controller`/`server`/`primary`. **Recommendation:** cosmetic; decide before it's
user-visible in CLI/docs. Low stakes.

## Smaller things to pin during implementation

- Route uniqueness key = `(host, listen_port, path_prefix)`, enforced cluster-wide
  via a unique index (`05`, `02`). `listen_port` defaults to 443; the container
  `service_port` is the forward target, **not** part of the key.
- Resource name scope = **project-wide** (a `database: main` is one thing per
  project, shared by name across shards/environments — `11`/`17`). A private
  resource just takes a distinct name; app-scoped private resources are not a
  separate concept in 0.1.0.
- ID format (ULID vs UUIDv7) — recommend ULID (sortable, compact, URL-safe).
- Default cluster domain handling (store a default wildcard so the wizard can offer
  `<slug>.<default>`?). Recommend: yes, a cluster setting.
- Checked-in generated code vs `make generate` — recommend checked-in for sqlc &
  proto to ease onboarding.
- Whether `skali` and `skalid` ship as one combined binary or two — plan assumes
  **two** (clear separation, smaller client). Confirm.
