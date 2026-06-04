# skali — Project Plan

This directory is the design reference for **skali**, a self-hostable **hosting
platform**: it deploys applications (the Coolify part) *and* manages their
databases and storage as first-class resources (the Sevalla part). It is written
*before* implementation so we have a single coherent description of the system to
build against. It is a living document: when the design changes, change these
files.

## How to read this

Start at `00-vision-and-scope.md`, then `01-architecture-overview.md`, then
**`17-resources-and-data.md`** (the core resource model — a Project is a bundle of
typed resources that bind together). After that the documents are mostly
independent and can be read on demand. `12-canonical-deploy-flow.md` is the best
way to see how everything fits together end to end; `DECISIONS.md` is the log of
what we chose and why.

| File | What it covers |
|------|----------------|
| `00-vision-and-scope.md` | What we're building, for whom, goals & non-goals, glossary |
| `01-architecture-overview.md` | The two binaries, control vs data plane, node roles, system diagram |
| `02-networking-and-routing.md` | Network contract, edge/node proxy model, Traefik roles, routing flow, container isolation |
| `03-orchestration-and-executor.md` | The `Executor` seam, local/remote, the reconciliation loop, Docker integration |
| `04-build-and-registry.md` | Build↔release decoupling, built-in & external registries, build location (builder node vs local build), promotion |
| `05-data-model-and-storage.md` | SQLite as source of truth, the resource schema (projects/applications/databases/volumes/bindings), goose + sqlc, secrets at rest |
| `06-rest-api.md` | chi REST API for clients, SSE log streaming, errors, versioning |
| `07-grpc-control-plane.md` | Node-to-node gRPC surface, streaming, mTLS |
| `08-auth-and-security.md` | User auth (opaque tokens), node auth (CA + enrollment + mTLS), threat model |
| `09-cli-skali.md` | The `skali` client (cobra + huh), command tree, contexts, environments & promote |
| `10-daemon-skalid.md` | The `skalid` daemon, roles, lifecycle, bootstrap, managed Traefik + registry |
| `11-project-config.md` | The `.skali/` directory; resources, requirements-list semantics, **sharded config** (polyrepo), environments |
| `12-canonical-deploy-flow.md` | End-to-end walkthrough of `skali deploy` (app + database + volume) |
| `13-webui.md` | SvelteKit + adapter-static, embedding into the daemon |
| `14-repo-layout-and-tooling.md` | Monorepo structure, codegen, dev workflow |
| `15-roadmap.md` | The 0.1.0 Definition of Done, versioned milestones, future ideas |
| `16-open-questions-and-risks.md` | Decisions **not** yet made and live design tensions |
| `17-resources-and-data.md` | The typed-resource/binding model; managed databases & volumes; disk-safety floor |
| `DECISIONS.md` | Append-only log of resolved decisions ("what we chose and why") |

## Status legend

Used throughout the docs to mark scope. **The authoritative 0.1.0 scope is the
"Definition of Done" at the top of `15-roadmap.md`** — these inline tags are
signposts, that list is the line in the sand.

- **[0.1.0]** — in scope for the first working version: the canonical *bundle*
  (app **+ database + volume**) deploying end to end on a single node (`15`).
- **[soon]** — planned shortly after 0.1.0; design should not preclude it.
- **[later]** — a clear next step on an existing mechanism (e.g. more DB engines).
- **[future]** — explicitly deferred; noted so the architecture leaves room.
- **[open]** — a decision we have not made yet; see
  `16-open-questions-and-risks.md`.

## Naming & terminology conventions

- **node** — a server running `skalid`.
- **cluster** — a set of nodes coordinated by one master.
- **master** — the node holding the control plane (REST API + SQLite source of
  truth + orchestrator). One per cluster. (Role name: `master`.)
- **edge** — the node(s) running the public-facing ingress proxy.
- **worker** — a node that runs application containers.
- **builder** — a node that builds images.
- In 0.1.0 a single node holds **all four roles**.
- **control plane** — traffic that manages skali itself (gRPC between daemons,
  the REST API). **data plane** — the actual end-user traffic to deployed apps.
- **Project = a bundle of typed Resources.** A Project is a logical system, not a
  single app. Its **resources** are `application` (the buildable one), `database`,
  and `volume`; a **Binding** wires them (app⟷database → injected `DATABASE_URL`;
  app⟷volume → a mount). See `17`.
- **Application → Environment → Release → Instance** — the **application** resource
  is the build unit; an Environment is a named realization of the bundle
  (`production`, `staging`, …) owning its routes/vars/resources **and its realized
  databases/volumes/bindings**; a Release is an immutable versioned desired-state
  for one environment (image + config snapshot); an Instance is one running
  container realizing a Release on a node. Every Project has a default `production`
  environment, so simple projects ignore this layer.
- **Stateful vs stateless** — application instances are stateless and recreatable;
  `database`/`volume` resources are **stateful**: pinned, provisioned-once, never
  blue/green or auto-rescheduled/pruned (`03`/`17`).

## Stack at a glance

- **Language:** Go (monorepo, one module, two binaries: `skali`, `skalid`).
- **CLI:** cobra + charmbracelet/huh (interactive config).
- **Web UI:** SvelteKit + adapter-static, embedded via `go:embed`.
- **Client API:** REST over chi + SSE for streaming.
- **Node-to-node:** gRPC, mTLS.
- **Storage:** SQLite on the master; goose migrations + sqlc queries.
- **Containers:** Docker via the official Go SDK (never shelling out).
- **Proxy:** Traefik at both edge and node tiers.
- **Registry:** built-in managed `registry:2` container (default, skalid-owned like
  Traefik); connect an external registry optionally.
- **Managed data:** Postgres database pools + quota-enforced disk volumes, both
  skalid-supervised like the registry; declared as resources and bound to apps
  (`17`). Object storage (S3) is `[future]`.
