# 07 — Roadmap & demolition

Milestones are vertical slices: each ends with something visible in the UI. App
before database on purpose — a stateless deploy exercises the whole new pipeline
(compile → SSA → status → ingress → UI) before stateful complexity joins.

## M0 — Demolition & substrate

**Goal: repo builds and runs `skalid serve` against a k3d cluster, doing nothing yet.**

Demolition checklist:
- [ ] Delete `internal/{cluster,clusterpb,engine,reconcile,mirror,hostinfo,leader,operations}`
- [ ] Delete `proto/`, `buf.yaml`, `buf.gen.yaml`, buf tooling from Taskfile
- [ ] Delete `skalid agent` mode + enrollment/agent config; collapse config (02)
- [ ] Delete migrations 00004–00010 + matching `query/*.sql`; regenerate sqlc
- [ ] Delete API handlers/routes for nodes*, containers, images, volumes, registry,
      operations, workloads (*nodes returns in M1 kube-backed)
- [ ] Web deletions per 06 (workloads/containers/registry pages, dead types/nav)
- [ ] Rewrite `README.md` (architecture + roadmap = this plan)
- Keep: auth, api framework, store/testdb, obs, crypt, cliconfig/client, web, CLI.

Substrate checklist:
- [ ] k3d dev cluster task targets (create/destroy/reset, CNPG operator install)
- [ ] `internal/kube`: client (kubeconfig|in-cluster), SSA apply helper, informer
      factory, label schema
- [ ] `deploy/`: pinned bootstrap kustomization (CNPG, skali-system, skalid) — may
      start as docs + manifests, `skali install` command later
- [ ] Prod bootstrap doc: k3s install (pinned channel) + Spegel/registries note

## M1 — Projects & service core

**Goal: create a project in the UI → namespace exists; service skeleton wired.**

- [ ] Migrations: projects, services, releases, connections (03)
- [ ] `internal/services` CRUD + validation; `/v1/projects...` routes (03)
- [ ] `internal/controller`: pass/poke loop, SSA apply + label prune, status rollup,
      per-service backoff (port of m5 patterns, kube-flavored)
- [ ] Compile: namespace per project
- [ ] Web: projects list/create real; kube-backed read-only nodes page
- [ ] Verify: create/delete project → namespace appears/prunes; controller restart-safe

## M2 — Applications end to end

**Goal: deploy a real app with a domain from the UI; logs visible; rollback works.**

- [ ] `service_applications` + releases flow: tag→digest resolve, release rows (04)
- [ ] Compiler: Deployment/Service/Ingress/PVCs + golden tests
- [ ] Rollout status → release + service status; rollback action
- [ ] Volumes v1 rule (replicas=1 + Recreate) enforced + explained in UI
- [ ] Logs proxy endpoint + logs tab; basic overview metrics (metrics-server)
- [ ] Web: application create/overview/settings/logs on the real API
- [ ] Verify: deploy nginx + a real app (sveltekit adapter-node) with volume +
      domain via k3d Traefik; kill pod → self-heal; bad image → failed release UX

## M3 — Databases on CNPG

**Goal: "PostgreSQL 17" in the UI → connectable database; app connects via UI.**

- [ ] `database_pools` + `service_databases` migrations; packing rule (05)
- [ ] Compilers: pool → CNPG Cluster (blessed image catalog, pgvector/postgis);
      database → Secret + managed role + Database CR; golden tests
- [ ] Status: pool health + Database readiness → service status
- [ ] Connection endpoint + panel (internal host/port/user/password reveal)
- [ ] Connections app↔db: env injection + secret mirror + new release on change (04)
- [ ] Password rotation action
- [ ] Dedicated-pool creation option
- [ ] Web: database create (Sevalla-style form), overview, connect-app flow
- [ ] Verify: two DBs land in one pool; dedicated lands alone; app reads/writes via
      injected env; rotate password → app follows; delete DB → Database CR gone,
      pool survives; delete last DB → shared pool GC'd

## M4 — Product polish

**Goal: daily-drivable.**

- [ ] TLS: cert-manager (pinned) + Let's Encrypt for app domains
- [ ] Analytics tabs: metrics history poller + charts (apps + databases)
- [ ] Releases/history UI polish; deploy-from-CLI (`skali deploy`, CLI v1 per 06)
- [ ] CNPG pool backups to S3 (WAL + scheduled base backups) — config, not yet UI
- [ ] Ops docs: k3s upgrades, skali upgrades, disaster recovery basics

## Later backlog (ordered-ish)

1. User-facing backups tab (manual/scheduled logical dumps + restore) — 05
2. External database connections (Traefik TCP/NodePort + allowlists)
3. Compose import (compose file → project of services) and templates/marketplace
   (curated Nextcloud-class apps; needs sidecars/cron first) — see discussion log
4. Sidecars + cron jobs on applications (the Nextcloud requirements)
5. cache (Valkey), object_storage (MinIO/Garage), static_site service types
6. Environments (prod/staging) + clone-data-from-prod (CNPG restore/replica)
7. Git builds (buildpacks + in-cluster registry), deploy on push
8. Per-project members/roles; API tokens for CI
9. RWX volumes (Longhorn) / multi-replica stateful apps; secret-typed env vars
10. HA control plane (k3s embedded etcd; skalid leader election returns if needed)
11. **`skali dev` (local runner)**: ephemeral k3d cluster + the same compilers over a
    project definition → full-fidelity local run (app + database) with port-forward
    and logs. Needs a serialized project definition (`skali.yaml`) — the file form of
    the service model, which then also serves GitOps and any future consumer.
    Design rule that keeps this free: compilers stay pure, `internal/kube` stays the
    only substrate-facing package. No abstract multi-backend SDK before a second
    consumer actually exists (cloud backends, if ever, arrive via Crossplane/ACK
    through the same kube API — not as a parallel runtime).

## Verification stance

Every milestone ends with the e2e loop on k3d driven through the real UI (dev user
dev@skali.local), not just unit tests: create → converge → break something → watch
it heal → delete → verify prune. Compiler golden tests are the regression net;
controller behavior is exercised against a live k3d cluster in CI-lite fashion
(task target), not mocked.
