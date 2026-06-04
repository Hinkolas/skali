# 00 — Vision & Scope

## Vision

skali is a self-hostable deployment platform: you run a daemon on your own
servers, point it at Docker, and deploy applications to them from a CLI or a web
UI. It is an open-source alternative to Coolify, designed from the start to
coordinate **multiple servers** as one cluster, with good developer experience
(`skali deploy` in a project directory just works) and a robust, boring
architecture underneath.

The guiding principle is **build it right, then extend**. We are not shipping a
throwaway MVP; 0.1.0 is the first real version of the eventual system. Where we
defer features, we make sure the architecture leaves room for them rather than
hard-coding single-node assumptions.

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

If that loop works on a single node, 0.1.0 is done.

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
| **Project** | A logical application — the *build* unit; has one or more Environments. |
| **Environment** | A named running configuration of a Project (`production`, `staging`, …): its own vars, routes, replicas, placement, resources, and release history. Every Project has a default `production` env. |
| **Build** | The act/record of turning source into an image digest (Project-level; normally shared across Environments). |
| **Release** | An immutable, versioned desired-state **for one Environment**: image digest + that env's config snapshot. |
| **Promote** | Release another Environment's already-built digest into this one — no rebuild. |
| **Instance** | One running container realizing a Release on a node (a replica). |
| **Route** | A host rule (e.g. `app.example.com`) attached to an Environment. |
| **control plane** | skali's own management traffic (gRPC + REST). |
| **data plane** | End-user traffic to deployed apps. |
| **advertise address** | The address peers use to reach a node over the cluster's private network. |
