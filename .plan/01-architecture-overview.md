# 01 — Architecture Overview

## Two binaries, one module

The Go monorepo produces two binaries that share `internal/` packages:

- **`skalid`** — the daemon. Runs on every node. Depending on its assigned
  **roles**, it exposes the REST API, runs the orchestrator, talks to the local
  Docker daemon, runs the gRPC control-plane server, and/or manages the proxy.
  Commands are plain cobra (`skalid start`, `skalid enroll`, …).
- **`skali`** — the CLI client. Runs on a developer's machine (or in CI). Talks
  to a master over the REST API. Uses cobra + charmbracelet/huh for interactive
  project configuration (`skali deploy`, `skali auth login`, …).

The **web UI** (SvelteKit, `adapter-static`) is compiled to static assets and
embedded into `skalid` via `go:embed`; the daemon serves it. There is **no
Node.js runtime on any server** — all data access goes through the Go REST API.

## Node roles

A node is not "a master" or "a worker" as a hard type; it holds a **set of
roles**. This keeps a single mental model from one node to a hundred.

| Role | Responsibility | Enables |
|------|----------------|---------|
| `master` | Control plane: REST API, SQLite source of truth, orchestrator, CA, built-in registry | One per cluster |
| `edge` | Public ingress: terminates TLS, routes `host → node` | ACME + Traefik HTTP provider |
| `worker` | Runs application containers, **database pools, and volumes** | Docker + Traefik Docker provider; pinned stateful data (`17`) |
| `builder` | Builds images, pushes to a registry | Docker/BuildKit |

In **0.1.0 a single node holds all four roles.** Adding a node later means
granting it `worker` (and maybe `builder`); promoting ingress capacity later
means granting another node `edge`. None of these require rearchitecting.

**Build location is decoupled from these roles.** Because builds are far heavier
than running the app, the `builder` role is granted only to *capable* nodes — and
the `skali` CLI can also build **locally on the developer's machine** and push the
image to the registry (`04`). So a weak cloud node runs apps while a powerful
laptop does the builds. This is the **one exception to "the CLI is a thin REST
client"**: in local-build mode `skali` drives local Docker and pushes to the
registry (with a short-lived, master-minted scoped token); everything else stays
pure REST. The build-and-push code is shared `internal/` used by both binaries.

## The two planes

This split drives the auth and networking design (see `02`, `08`).

- **Control plane** — skali managing itself.
  - Client ↔ master: **REST** (chi) + SSE, authenticated with **user tokens**.
  - Master ↔ node: **gRPC**, authenticated with **mTLS** (the master's CA signs
    every node's identity at enrollment).
  - Low volume, high authority (it can run arbitrary containers fleet-wide).
- **Data plane** — end-user traffic to deployed apps.
  - `client → edge proxy → node proxy → container`.
  - Trusts the private network (see the network contract in `02`); internal hops
    are plain HTTP by default. Public TLS terminates at the edge.

> Mental rule: the private network protects you from outsiders; control-plane
> mTLS protects you from the untrusted code you deliberately run inside it.

## System diagram (multi-node)

```
                          ┌────────────────────────────────────────────┐
   developer machine      │                 CLUSTER                     │
  ┌───────────────┐       │                                            │
  │  skali (CLI)  │       │   ┌───────────── node A (master,edge) ───┐  │
  │  web browser  │──REST─┼──▶│  skalid:                             │  │
  └───────────────┘  TLS  │   │   • REST API (chi) + SSE             │  │
        ▲                 │   │   • SQLite (source of truth)         │  │
        │ HTTPS (apps)    │   │   • orchestrator / reconciler        │  │
        │                 │   │   • CA + gRPC server (mTLS)          │  │
        │                 │   │   • edge Traefik  (:80/:443, ACME)   │  │
        │                 │   │   • built-in registry (:5000)        │  │
        └─────────────────┼──▶│   • local worker Traefik + Docker    │  │
                          │   └───────────────┬──────────────────────┘  │
                          │            gRPC (mTLS) │  data plane (HTTP)  │
                          │                        ▼  over private net   │
                          │   ┌──────────── node B (worker) ──────────┐  │
                          │   │  skalid:                              │  │
                          │   │   • gRPC client/server (mTLS)         │  │
                          │   │   • worker Traefik (Docker provider)  │  │
                          │   │   • Docker: app containers            │  │
                          │   └───────────────────────────────────────┘  │
                          └────────────────────────────────────────────┘
```

In 0.1.0, only node A exists and the data-plane path collapses to
`client → (edge+worker Traefik) → container` with no internal hop.

## Layered code architecture

Transport stays thin; the multi-node seam is a single interface.

```
transport/      REST (chi) for clients · gRPC server for nodes
                — decode, authn/authz, call service, encode/stream. No logic.
service/        domain logic: Projects (bundles), Applications, Databases,
                Volumes, Bindings, Environments, Builds, Releases, Auth,
                orchestration. — transport-agnostic. The reconciler lives here.
executor/       Executor interface — THE multi-node seam.
                ├─ LocalExecutor  → Docker Go SDK on this host
                └─ RemoteExecutor → gRPC to another node's daemon
store/          sqlc-generated repository over SQLite (master only)
proxy/          renders edge config; stamps Traefik labels on containers
ca/             cluster CA: issue/sign/verify node certs
```

The service layer never touches Docker directly — it calls `Executor`. For the
local node that's `LocalExecutor` (direct Docker calls, **short-circuiting**
gRPC). For a remote node that's `RemoteExecutor` (a gRPC client). The remote
node's gRPC server is itself a thin wrapper that calls *its* `LocalExecutor`. So
the Docker-touching code is written **once** and reused at both ends. See `03`.

## Key invariants

1. **SQLite on the master is the single source of truth.** Application workers are
   **stateless** — they hold only running containers, reconciled toward the desired
   state on the master. **Carve-out:** `database` and `volume` resources are
   **stateful** — pinned to a node, provisioned once, never blue/green, never
   auto-rescheduled or auto-pruned. The master DB still describes them, but their
   on-node data is authoritative and is never destroyed-to-recreate (`03`/`17`).
   This is the deliberate exception that makes managed data possible.
2. **The deploy unit is an image digest, never a build context.** Builds produce
   digests; releases reference them. This makes nodes stateless, rollback trivial,
   and lets one image be **promoted** across environments without rebuilding (`04`).
3. **Routing config is derived, not authored by hand.** The master renders the
   edge proxy config from the DB; workers self-configure from Docker labels the
   daemon stamps. Nobody writes proxy config manually.
4. **One Traefik image everywhere.** Behavior differs only by which providers a
   node's roles enable.
5. **The local node is short-circuited.** "One coherent command structure" lives
   at the `Executor` interface, not by forcing localhost gRPC.
