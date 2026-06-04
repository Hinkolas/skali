# 00 — Vision & Scope

## Vision

skali is a self-hostable **hosting platform**: you run a daemon on your own
servers, point it at Docker, and manage whole applications — code, databases, and
storage — from a CLI or a web UI. You run the daemon on multiple servers and it
coordinates them as one cluster, with good developer experience (`skali deploy` in
a project directory just works) and a robust, boring architecture underneath.

The reference point is two-sided. Like **Coolify**, skali deploys your app from a
Dockerfile to a live HTTPS URL on your own hardware. But the larger goal is the
**Sevalla** idea: a project isn't just an app — it's an app **plus its managed
database plus its storage**, declared together and wired together, so you stop
hand-rolling a `compose.yaml` with a bespoke Postgres + a bespoke backup script +
unbounded volumes for every project. skali manages those as first-class
**resources** (`17`).

> The canonical example: a SvelteKit app that needs a Postgres database for
> product data and a 10Gi volume for product images. You declare all three, bind
> them, and skali provisions the database (with backups), enforces the volume's
> size limit, and injects `DATABASE_URL` into the app — no per-project database or
> backup toil.

The guiding principle is **build it right, then extend**. We are not shipping a
throwaway MVP; 0.1.0 is the first real version of the eventual system. Where we
defer features, we make sure the architecture leaves room for them rather than
hard-coding single-node — or single-resource-type — assumptions.

## The canonical flow (the thing 0.1.0 must deliver)

1. A developer builds a project (e.g. a website) and adds a `Dockerfile`.
2. They run `skali auth login` once to authenticate the CLI with their master.
3. They run `skali deploy` in the project directory.
4. Because the project has no `.skali/` config yet, skali asks a few interactive
   questions (project name, domain, target node, …), like a framework
   scaffolder.
5. skali builds the Docker image — on a **builder node** or **locally on the dev
   machine** (`build.builder`, see `04`) — pushes it to the built-in registry,
   creates a release (in the default `production` environment), runs it as a
   container, and wires it into the proxy so it is reachable at the chosen URL over
   HTTPS.
6. skali writes the assigned project id back into `.skali/` so the directory is
   linked for future deploys, the web UI, and later git push-to-deploy.

This application loop is the **spine**. 0.1.0 then extends it **horizontally** to
the other two resource pillars — a bound **database** (provisioned, with backups)
and a quota-enforced **volume** — so the canonical *bundle* (app + db + storage)
works end to end, not just the app. The breadth-first 0.1.0 scope is the
Definition of Done in `15`; the resource model is `17`.

## Target users

- Developers and small teams who want Heroku/Vercel-like ergonomics on their own
  hardware (homelab, office servers, a handful of cloud VPS).
- People who already use Docker + a reverse proxy and want orchestration,
  multi-server management, and a nice deploy UX on top — without adopting
  Kubernetes.

## Goals

- **Multi-node from day one (in design).** One master coordinates many nodes;
  the model, data layer, and APIs assume a cluster even when only one node
  exists.
- **Great deploy DX.** `skali deploy` with zero prior config should be enough to
  go from a Dockerfile to a live, HTTPS URL.
- **One wildcard, any node.** Point a wildcard DNS record at one IP and have
  every app reachable regardless of which node hosts it.
- **In-house routing & load balancing** across replicas, without depending on a
  cloud provider's load balancer.
- **Image-based deploys.** Builds produce images in a registry; deploys run
  images. This unifies "build then deploy" and "deploy an existing image."
- **Managed data as first-class resources.** Databases and volumes are declared
  alongside the app and **bound** to it (`17`): skali provisions a database,
  injects `DATABASE_URL`, and runs scheduled backups; the *allocation* (a shared
  pool vs a dedicated server) is a placement knob decoupled from the declaration.
  No per-project Postgres-in-Compose + bespoke backup script.
- **Storage that can't take down the host.** Every volume has an **enforced size
  limit** and lives on a partition isolated from host/control data with reserved
  headroom — a runaway volume fills its own quota, never the disk (`17`). This is
  a 0.1.0 safety invariant, not a deferred nicety.
- **Build where it's fast, run where it's cheap.** Build location is decoupled from
  run location: a weak app node never has to build. Builds run on a capable
  `builder` node *or* locally on the developer's machine (e.g. a fast laptop),
  pushing the image to the registry — `build.builder` per project (`04`).
- **Self-contained daemon.** A single binary lands on a server and runs; it
  migrates its own database, manages its own certificates, and needs no Node.js
  or external services to operate.
- **Defense in depth.** The data plane trusts the private network; the control
  plane authenticates regardless, because we run untrusted user code inside that
  network.

## Non-goals (for now)

- **Not Kubernetes.** No pod scheduling theory, no CRDs, no etcd. Docker +
  Traefik is the core stack. (Podman/k8s/cloud backends are `[future]`.)
- **Not multi-master HA in 0.1.0.** A single master holds the source of truth.
  The data plane survives a master outage (running apps keep running), but
  control-plane HA is `[future]` — see `16-open-questions-and-risks.md`.
- **Not a general PaaS marketplace.** No one-click app catalog in 0.1.0.
- **Not object storage (S3) in the runtime model.** The 0.1.0 storage pillar is
  **disk/volumes only**. External object stores (S3/R2/MinIO) are `[future]` and
  arrive as a *connect-and-surface-in-UI* convenience (like an external registry),
  **not** a provisioned/mounted resource type. (`16`/`17`.)
- **Not managed-database engine zoo in 0.1.0.** Postgres first; MySQL/Redis and
  others are `[later]` on the same pool/allocation model (`17`).
- **No automatic cross-node rescheduling on node failure in 0.1.0.** Placement
  is declarative; self-healing across a *dead node* is `[soon]/[future]`. (The
  reconciliation loop *does* recover crashed containers on a *live* node — see
  `03`.)
- **No cross-architecture builds in 0.1.0.** Hosts report their arch
  (`amd64`/`arm64`); a build's arch must match the target nodes' arch, else the
  master errors. Emulated cross-build (a powerful arm64 box producing amd64 via
  QEMU) is `[future]` — see `04`.

## Glossary

| Term | Meaning |
|------|---------|
| **node** | A server running `skalid`. |
| **cluster** | A set of nodes coordinated by one master. |
| **master** | The control-plane node: REST API + SQLite source of truth + orchestrator. |
| **edge** | Node role: runs the public ingress proxy. |
| **worker** | Node role: runs application containers. |
| **builder** | Node role: builds images and pushes them to a registry. |
| **Project** | A **bundle** — a logical system (app + its database + its storage). Owns typed Resources and one or more Environments. (`17`) |
| **Resource** | A typed member of a bundle: `application`, `database`, or `volume`. (`17`) |
| **Application** | The **buildable** resource — source → image → containers. 1 per Project in 0.1.0; carries the build config. |
| **Database** | A managed logical database (Postgres) realized per Environment on a pool; allocation = shared / dedicated / dedicated-host. (`17`) |
| **Volume** | A quota-enforced disk realized per Environment, pinned to a node, mounted into a bound application. (`17`) |
| **Binding** | Wires an Environment's Application to a Database (→ injected `DATABASE_URL`) or Volume (→ mount path). N apps may share one resource. |
| **DatabasePool** | A skalid-supervised engine container hosting one (dedicated) or many (shared) logical databases. |
| **Environment** | A named realization of the bundle (`production`, `staging`, …): its own vars, routes, replicas, placement, resources, **databases/volumes/bindings**, and release history. Every Project has a default `production` env. |
| **Build** | The act/record of turning source into an image digest (**Application**-level; normally shared across Environments). |
| **Release** | An immutable, versioned desired-state **for one Environment**: image digest + that env's config snapshot (incl. injected binding values). |
| **Promote** | Release another Environment's already-built digest into this one — no rebuild. |
| **Instance** | One running container realizing a Release on a node (a replica). |
| **Route** | A host rule (e.g. `app.example.com`) attached to an Environment. |
| **control plane** | skali's own management traffic (gRPC + REST). |
| **data plane** | End-user traffic to deployed apps. |
| **advertise address** | The address peers use to reach a node over the cluster's private network. |
