# 01 — Substrate decision: k3s

## The decision

skali runs on **k3s** (single-binary Kubernetes). skali itself becomes a product
control plane: a Go service that stores user intent in Postgres, compiles it into
Kubernetes objects, applies them, and reads status back. All generic orchestration —
scheduling, restarts, rolling updates, cross-node networking, DNS, service discovery,
ingress, storage attachment — is k3s's job. All stateful database operations —
replication, failover, backups, PITR — are CloudNativePG's job.

## Why (recorded so we don't relitigate)

1. **The fungibility problem.** Orchestrators derive their power from treating
   containers as fungible. Data breaks that twice: state ties a container to a disk on
   a node, and database replicas have *roles* (primary/standby), making failover an
   application-specific ritual. Encoding that knowledge is what operators are.
   Building managed databases without Kubernetes means rebuilding both the generic
   layer (≈ Kubernetes) and the database layer (≈ CNPG). m2–m5 were the first clone;
   the second one is years of work and never the product.
2. **The product we're modeling is already k8s.** Sevalla's own internal hostnames are
   `*.svc.cluster.local` — it is a UI over Kubernetes. Porter and Northflank likewise.
   The swarm-based tier (CapRover, Dokploy) is exactly the tier without a serious
   managed-database story.
3. **The "k8s is inflexible" fear is a category error.** The pain of k8s is humans
   writing YAML and operating clusters by hand. skali is a *program* generating API
   objects — for that client, k8s is the most programmable infra API there is. Users
   never see YAML; skali owns 100% of the UX. Where primitives don't fit, controllers
   and CRDs are the sanctioned extension mechanism — skali effectively *is* an
   external operator.
4. **CNPG is the single biggest lever.** HA Postgres, automated failover, WAL
   archiving, PITR backups to object storage, replica/clone clusters. That is skali's
   entire replication/environments roadmap as a dependency instead of a multi-year
   build.
5. **Swarm rejected**: maintenance-mode ecosystem, no operators, no volume
   orchestration — weakest exactly where skali needs the most (stateful).
   **Standalone Calico/flannel + own DNS rejected**: unsupported integration path
   (Docker speaks CNM, not CNI), months of infra work, zero product value.
6. **Multi-backend (AWS etc.) stays possible** through the compiler seam — and the
   industry route for external clouds is Crossplane/ACK, which is driven *through* the
   kube API anyway. k3s is not one backend among many; it's the integration point.

Accepted costs: ~512MB–1GB RAM for the k3s server and a few hundred MB per agent;
debugging moves from `docker ps` to `kubectl`; we inherit k8s upgrade cadence.
Docker-compose apps are handled as an **import format** (compose → skali services),
never as a second runtime substrate.

## Rework in place — not a git rollback, not a greenfield repo

The question "jump back to before the business logic?" has a clear answer: **no**.
The commits after m1 interleave two things: substrate code (which dies) and product
code (which is precisely what we keep — the whole mock services UI, the workloads
page patterns, component library growth, auth/UX refinements, CLI plumbing). A
rollback destroys the parts we want; a fresh repo loses history and forces
re-importing auth/web anyway. The layering is clean enough that the substrate
packages delete surgically.

So: **prune forward on `main`** (or a short-lived `rearch` branch). Delete the dead
packages in one commit (M0), keep everything else, let git history be the archive.

## What survives / what dies

| Area | Verdict | Notes |
| --- | --- | --- |
| `internal/auth`, migrations 00001–00003 | **Keep** | Untouched. |
| `internal/api` framework (router, middleware, error mapping, OpenAPI embed) | **Keep** | Handlers for dead resources go; conventions stay. |
| `internal/store`, sqlc/goose/pgx setup, `internal/testdb` | **Keep** | Queries for dead tables go. |
| `web/` | **Keep all** | Mock services UI becomes the real product surface (06). |
| `cmd/skali`, `internal/cliconfig`, `internal/client` | **Keep** | CLI concept retained. |
| `internal/obs`, `internal/crypt`, `internal/config`, `internal/version` | **Keep** | Config shrinks (cluster/mirror settings go). |
| `internal/cluster` (CA, enrollment, heartbeat, poller, watcher, handles) | **Delete** | k3s join tokens + kube API replace all of it. |
| `internal/clusterpb`, `proto/`, `buf.yaml`, `buf.gen.yaml` | **Delete** | No gRPC fabric. |
| `internal/engine` (docker adapter, sampler, inventory, events) | **Delete** | kubelet/containerd + metrics-server + informers. |
| `internal/reconcile` (workloads) | **Delete** | Replaced by `internal/compile` + `internal/controller` (02). The level-triggered pass + poke *pattern* survives; the body shrinks drastically. |
| `internal/mirror` | **Delete** | k3s `registries.yaml` + embedded Spegel mirror cover distribution. Digest *pinning* survives as product policy: resolve tag→digest at deploy time (go-containerregistry), pin pod specs. |
| `internal/hostinfo` | **Delete** | Node facts/metrics come from the kube API + metrics-server. |
| `internal/leader` | **Delete** | Control plane runs single-replica in v1. Reintroduce (same PG advisory-lock trick) only if we ever run it HA. |
| `internal/operations` | **Delete** | Long-running work is now watchable k8s status; the 202+operations pattern can return later if a real async task appears. |
| Migrations 00004–00010 + their `query/*.sql` | **Delete/squash** | No production deployments exist; new baseline migrations for the service model. |
| `skalid agent` mode | **Delete** | No skali agent binary. Nodes run k3s only. |

## Tech-debt notes this pivot erases for free

- Custom mTLS CA + cert rotation semantics (`AUTH_SECRET` rotation orphaning nodes).
- Heartbeat/poller/sampler timing interplay and present/absent report semantics.
- Doorbell chain (engine events → gRPC stream → watcher → poke fan-out).
- Registry mirror ensure-loop, certs.d distribution, GC deferral.
- Workload assignment phase machine and adoption/orphan edge cases.

Each was well-built; none is product. This is where "it feels like I'm cloning
Kubernetes" was exactly right.
