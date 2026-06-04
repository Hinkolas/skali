# skali — Project Plan

This directory is the design reference for **skali**, a self-hostable deployment
platform (an open-source alternative to Coolify). It is written *before*
implementation so we have a single coherent description of the system to build
against. It is a living document: when the design changes, change these files.

## How to read this

Start at `00-vision-and-scope.md`, then `01-architecture-overview.md`. After that
the documents are mostly independent and can be read on demand. The
`12-canonical-deploy-flow.md` is the best way to see how everything fits together
end to end.

| File | What it covers |
|------|----------------|
| `00-vision-and-scope.md` | What we're building, for whom, goals & non-goals, glossary |
| `01-architecture-overview.md` | The two binaries, control vs data plane, node roles, system diagram |
| `02-networking-and-routing.md` | Network contract, edge/node proxy model, Traefik roles, routing flow, container isolation |
| `03-orchestration-and-executor.md` | The `Executor` seam, local/remote, the reconciliation loop, Docker integration |
| `04-build-and-registry.md` | Build↔release decoupling, built-in & external registries, build location (builder node vs local build), promotion |
| `05-data-model-and-storage.md` | SQLite as source of truth, schema, goose + sqlc, secrets at rest |
| `06-rest-api.md` | chi REST API for clients, SSE log streaming, errors, versioning |
| `07-grpc-control-plane.md` | Node-to-node gRPC surface, streaming, mTLS |
| `08-auth-and-security.md` | User auth (opaque tokens), node auth (CA + enrollment + mTLS), threat model |
| `09-cli-skali.md` | The `skali` client (cobra + huh), command tree, contexts, environments & promote |
| `10-daemon-skalid.md` | The `skalid` daemon, roles, lifecycle, bootstrap, managed Traefik + registry |
| `11-project-config.md` | The `.skali/` directory, environments block, committed vs local state |
| `12-canonical-deploy-flow.md` | End-to-end walkthrough of `skali deploy` |
| `13-webui.md` | SvelteKit + adapter-static, embedding into the daemon |
| `14-repo-layout-and-tooling.md` | Monorepo structure, codegen, dev workflow |
| `15-roadmap.md` | Versioned milestones and future feature ideas |
| `16-open-questions-and-risks.md` | Unresolved decisions and design tensions to revisit |

## Status legend

Used throughout the docs to mark scope:

- **[0.1.0]** — in scope for the first working version (the canonical
  `auth login` → `deploy` flow on a single node).
- **[soon]** — planned shortly after 0.1.0; design should not preclude it.
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
- **Project → Environment → Release → Instance** — a Project is the logical app
  (the build unit); an Environment is a named running configuration of it
  (`production`, `staging`, …) owning its routes/vars/resources; a Release is an
  immutable versioned desired-state for one environment (image + config); an
  Instance is one running container realizing a Release on a node. Every Project
  has a default `production` environment, so simple projects ignore this layer.

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
