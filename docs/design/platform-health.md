# Platform health proposal

Unscheduled design proposal, preserved from [issue #108](https://github.com/Hinkolas/skali/issues/108)
when separating roadmap directions from implementable work. This is not an
implementation contract; illustrative APIs and manifest fields are proposals,
not supported interfaces. The issue retains the original discussion history.

This proposal is for after the first stable release. Nothing here is scheduled yet.

Cluster admins can see the health of individual projects, but not of the platform underneath them. A project's bucket can be healthy while the S3 service itself is not fully operational, and today nothing tells the operator.

This came up after upgrading a cluster from rc.10 to rc.11. The first bucket backup failed with `substrate: read platform credentials: secrets "seaweed-platform" not found`. rc.11 introduced the platform S3 identity. Its Secret is created at the end of the object-store reconcile pass, and only once the store reports ready. Until then, every bucket backup fails. Nothing in Studio showed that object storage was still converging, and if that pass had kept failing, the only trace would have been skalid's logs.

## What exists

- **Object storage.** `SeaweedProbe` (`internal/substrate/observe.go`) already reports masters, volume servers, filer, S3 gateway readiness, under-replicated and replication-pending volumes, and placement shortfalls (`module.ObjectStoreStatus`). Studio does not show any of it.
- **Observation plane.** `GET /v1/system/observation` reports connected or API-only mode, cache readiness, watch synchronization, source freshness and reconcile queue depth. Studio does not show this either.
- **Database pools.** `/v1/system/database-pools` and its metrics are the one subsystem with real health in Studio.
- **Studio System hub.** Its Nodes, Software update, Databases and Backup target cards are settings pages with a status pill, not a health view.
- **Reconcile failures.** A controller pass that fails repeatedly at the same step is only visible in the logs.

## Proposal

### 1. One health model for every subsystem

Each subsystem reports:

- a state: `operational`, `degraded`, `down` or `converging` (still reconciling, for example right after an upgrade);
- a one-line summary;
- a list of checks, each with a pass/fail result, a reason, and how long it has been in that state;
- the result and time of its last reconcile pass.

All of it is served from one admin-only endpoint, `GET /v1/system/health`, with a stream variant like `/v1/environments/{id}/status/stream`.

### 2. Subsystems and checks

| Subsystem | Checks |
|---|---|
| Control plane | skalid's metadata database, observation sync, reconcile queue depth, items retrying for a long time |
| Nodes | online count, host agents reporting in, disk and memory pressure |
| Object storage | masters, volume servers, filer, S3 gateway, platform identity loaded, replication, placement |
| Database pools | existing pool health, summarized |
| Edge | ingress controller ready, certificates close to expiry or failing to renew |
| Registry and builds | registry reachable, build workers available |
| Backups | backup target reachable, most recent successful backup per scheduled environment, missed schedules |
| Updates | an update stuck partway or needing a resume |

### 3. Reconcile results count as health

Each controller records the outcome of its last pass. A pass that keeps failing at the same step for more than a few minutes turns its subsystem `degraded` and shows that step's error. With this in place, the rc.11 upgrade above would have shown something like "Object storage: converging, platform identity not provisioned yet".

### 4. Operations check their dependencies before they start

Backups, restores and credential rotation check the subsystems they depend on and refuse with a clear reason, such as "object storage is still converging after an upgrade", instead of failing partway with a raw Kubernetes error.

### 5. Studio

- A **System → Status** page with a summary banner ("All systems operational" or "1 subsystem degraded"), one expandable row per subsystem with its checks, and recent state changes.
- An admin-only line under the "nodes online" indicator in the sidebar footer, such as "Object storage degraded", linking to the page.
- Optionally, a neutral notice on project pages when a subsystem they depend on is not operational ("bucket operations may fail right now"), without exposing platform details to non-admins.

### 6. Later

- A history of state changes as a simple incident timeline.
- A per-subsystem up/down metric exported from `internal/metrics`.
- Notifications on state changes.

## Suggested order

1. Health model, endpoint and reconcile-result recording, covering control plane, object storage (including the platform identity) and database pools. Dependency checks for backup and restore.
2. Studio status page and the sidebar line.
3. The remaining subsystems (edge, registry and builds, backups, nodes, updates) and the state history.
