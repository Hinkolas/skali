# skali rearchitecture — plan

skali pivots from a hand-rolled orchestrator to a **product control plane on top of
Kubernetes (k3s)**. skali keeps everything that is product — the Sevalla-style services
concept, the web UI, the CLI, auth, the UX — and delegates everything that is substrate
(scheduling, networking, DNS, health, storage attachment, stateful database operations)
to k3s and a small set of blessed operators.

One sentence: **skali is the opinionated product layer that Kubernetes doesn't ship —
projects, applications, managed databases, and the UX around them.**

## Documents

| Doc | Contents |
| --- | --- |
| [01-decision-substrate.md](01-decision-substrate.md) | Why k3s, what survives / what dies, rework vs rollback |
| [02-architecture.md](02-architecture.md) | Components, control loop, package layout, bootstrap, dev environment |
| [03-service-model.md](03-service-model.md) | Projects, services, releases, data model, compilation contract, status |
| [04-applications.md](04-applications.md) | Application service type: spec, deploys, ingress, volumes, logs |
| [05-databases.md](05-databases.md) | Database service type: CNPG, pools, provisioning, credentials, backups |
| [06-web-cli.md](06-web-cli.md) | What the UI keeps, mock → real mapping, navigation, CLI scope |
| [07-roadmap.md](07-roadmap.md) | Milestones M0–M4, later backlog, demolition checklist |

## Principles (KISS, made concrete)

1. **Two sources of truth, one direction.** Postgres holds user intent (what should
   exist). Kubernetes holds machine state (what does exist). skali's controller only
   ever compiles intent → manifests and reads status back. No third place for state.
2. **Wrap, don't rebuild.** Scheduling, networking, DNS, ingress, certificates, and
   database HA/backup mechanics come from k3s, Traefik, cert-manager, and CloudNativePG.
   skali writes zero lines of failover, overlay, or CA code.
3. **No abstraction before the second consumer.** One substrate backend (k8s). The
   compiler seam (`services → manifests`) keeps future backends possible; we do not
   design an interface for backends that don't exist.
4. **One deployable.** `skalid` = API + controller in one binary. No agents, no gRPC
   fabric, no proto. The kube API is the cluster fabric.
5. **Typed columns over JSONB blobs** wherever sqlc can carry it. JSONB only for
   genuinely open-ended lists (env vars, domains).
6. **Delete, don't deprecate.** Dead code is removed in M0; git history is the archive.
7. **Blessed components, pinned versions.** Every third-party operator/manifest we
   install is version-pinned and installed by skali's bootstrap, not by hand.
8. **Honest guarantees.** Where a feature is soft (per-database storage quota in a
   shared pool) the docs and UI say so, rather than pretending.

## What is explicitly kept

- **Sevalla-style services concept** as designed in the mock UI (`web/src/lib/mock/`).
- **Web UI**: SvelteKit BFF, the whole component library, the base UX, navigation.
- **Auth (m1)**: users, sessions, TOTP 2FA, sudo/reauth. Untouched.
- **CLI concept** (`cmd/skali`).
- **Conventions**: chi + sqlc + goose + pgx, handler→service→store layering,
  go-task tooling, snake_case JSON, UUIDv7 ids.

## What is explicitly gone

The custom substrate: node system + CA + enrollment (m2), Docker engine adapter (m3),
registry mirror as infrastructure (m4), workload reconciler + leader lease (m5),
proto/gRPC cluster fabric. Details and rationale in
[01-decision-substrate.md](01-decision-substrate.md).
