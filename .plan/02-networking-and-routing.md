# 02 — Networking & Routing

## The network contract (bring your own network)

skali does **not** manage a VPN/overlay. It assumes:

> **All nodes in a cluster are mutually reachable over a network the operator
> trusts as private.** How that network is provided — office LAN, a cloud
> provider's private network (e.g. Hetzner vSwitch), or a user-managed WireGuard
> mesh — is outside skali's scope and documented as a setup prerequisite.

Consequences:

- The **control plane** (node↔node gRPC) is mTLS-secured and therefore safe over
  *any* network. It does not rely on the network being private.
- The **data plane** (edge→node→container) is plain HTTP and *does* rely on the
  network being trusted. This is acceptable because the operator asserts the
  network is private. Optional data-plane mTLS between proxies is a `[future]`
  hardening knob (we already have a CA, so it is cheap to add) for operators
  whose "private" network they don't fully trust.

This mirrors how Nomad draws the line; we deliberately stay out of the
network-plumbing business.

### Per-node addressing

Each node records, at enrollment:

- **`advertiseAddr`** — the address *peers* use to reach it over the private
  network (often **not** the public IP; e.g. a `10.x` Hetzner private address).
  Used to generate proxy backends and to dial gRPC.
- **`publicAddr`** *(edge role only)* — where the public wildcard DNS points;
  where end users connect. Most nodes don't need one.

Only **edge** nodes need a public IP. Workers can be entirely private — zero
public attack surface.

## Routing model: two-tier proxy, both Traefik

```
user ──https──▶ EDGE proxy ──http(private)──▶ NODE proxy ──http(docker net)──▶ container
                (TLS term)     (host → node)     (host → container)
```

Responsibilities split so each tier is simple:

- **Edge proxy** (`edge` role): terminates public TLS (ACME), routes on hostname
  → **node**. Its routing table is `host → advertiseAddr:nodeProxyPort`. It is
  **small and stable** — it changes only when an app *moves nodes*, not when
  containers churn. Config comes from the master via Traefik's **HTTP provider**
  (see below).
- **Node proxy** (`worker` role): one Traefik per node on a fixed private port,
  using Traefik's **Docker provider**. It auto-discovers local containers from
  the **labels the daemon stamps** on them and routes hostname → container by
  container IP on the shared Docker network. **No host ports are published for
  app containers.**
- **Container**: lives only on an internal Docker network. No published ports.

### Why no host ports

Binding each container to a host port would force the master to track per-node
port allocation in SQLite (collisions, reclamation on restart, reassignment) —
stateful coordination and a bug source. The two-tier proxy eliminates it: the
only published ports anywhere are the edge's `:80/:443` and each node proxy's one
fixed port. Container discovery is via Docker labels, the native Traefik model.

### Roles compose on one instance (the single-node collapse)

A node's proxy behavior is just which providers its roles enable:

| Roles on node | Providers enabled | Data-plane path |
|---------------|-------------------|-----------------|
| `worker` only | Docker | edge → this proxy → container |
| `edge`+`worker` (0.1.0) | Docker **+** HTTP + ACME | `user → proxy → container` (no hop) |
| `edge` only (future dedicated edge) | HTTP + ACME | terminates, forwards to worker proxies |

**Self-loop rule:** when edge and worker co-locate, the same Traefik runs both
the Docker provider (local containers) and the HTTP provider (the edge
host→node map). The master must emit edge-map entries **only for apps on
*remote* nodes** — apps on the edge node itself resolve directly via their
Docker labels. Otherwise a hostname would have two routers (Docker label +
HTTP-provider entry) pointing the proxy at itself. Each hostname therefore has
exactly one router source.

## How routing config is produced

- **Edge:** the master exposes an **HTTP-provider endpoint** (e.g.
  `GET /internal/traefik/http?node=<id>`) that renders Traefik dynamic config
  from SQLite: for every Route on a *remote* app, a router (host rule) → a
  service whose servers are the hosting node(s)' `advertiseAddr:nodeProxyPort`.
  Traefik polls it. SQLite stays the single source of truth; no hand-written
  config.
- **Node:** the daemon, when it reconciles an Instance, creates the container via
  the Docker SDK **with Traefik labels** (host rule, service port, optional
  middleware). The local node Traefik (Docker provider) picks them up
  automatically. The master generates *nothing* for node proxies.

### The routing key (and global host uniqueness)

A Route matches on **`(host, listen_port, path_prefix)`** — the *external*
entrypoint, not the container port:

- **`listen_port`** is the edge entrypoint the request arrives on (default `443`).
- **`service_port`** is the *internal* container port we forward to — a target,
  never part of the match.

So "multiple services on one domain" is expressed two ways, both covered by one
key: **by path** (`example.com/` → web, `example.com/api` → API) or **by external
port** (`example.com:443` → web, `example.com:8443` → admin). The latter just needs
the edge to open the extra entrypoint; for 0.1.0 only `:443`/`:80` are opened by
default, and non-standard listen ports (and raw TCP) are a small extension that
needs no schema change. A **cluster-wide `UNIQUE(host, listen_port, path_prefix)`**
constraint (`05`) makes a hostname resolve to exactly one service per port+path —
the master rejects a Route that would collide (e.g. two environments both claiming
`example.com:443/`), which is also why `staging.example.com` and `example.com` are
naturally distinct.

This is also where **replica load balancing** comes from for free:

- **Replicas on the same node** → the node proxy sees N containers with identical
  labels and round-robins them (with health checks).
- **Replicas across nodes** → the master lists all hosting nodes as servers for
  that host in the edge config; the edge round-robins across nodes.

Both tiers do health-aware L7 load balancing (Traefik active health checks —
free, unlike nginx OSS).

## TLS / certificates

- Public TLS terminates **once, at the edge**.
- **0.1.0 cert strategy: per-host certificates via HTTP-01.** The user points a
  **wildcard DNS record** (`*.apps.example.com`) at the edge for *routing*;
  certificates are still obtained **per hostname** via the ACME HTTP-01
  challenge on first request. This needs **no DNS provider credentials**.
  - Important distinction we must not blur: *wildcard DNS* (routing) ≠ *wildcard
    certificate* (TLS). A wildcard **cert** requires the **DNS-01** challenge,
    which requires giving skali API access to the DNS provider.
- **[soon/future] Wildcard certificates via DNS-01** as an option, behind a
  pluggable DNS-provider integration (Cloudflare, etc.). Reduces cert count and
  enables certs for not-yet-requested subdomains.
- Custom user domains (`mycustomdomain.com`) work identically via Host routing +
  HTTP-01.

## Access protection (ingress auth) `[soon]`

Sometimes a route must **not** be publicly reachable — e.g. a staging site shared
with a customer for review. This is an **ingress access policy** attached to a
**route or environment** and rendered as a **Traefik middleware** (the same way
routing labels are stamped, `05`/`11`) — so it's declarative config, not bespoke
code.

- **Basic-auth (first, `[soon]`).** A per-route/per-environment `users` list
  (`user:bcrypt-hash`) becomes a Traefik `basicAuth` middleware. Enough to gate a
  staging URL behind a username/password without making it public. Credentials are
  stored hashed server-side (htpasswd-style), so they're not plaintext secrets.
- **`[future]` stronger options**, behind the same "access policy" field:
  - **IP allowlist** (Traefik `ipAllowList`) — restrict to office/VPN ranges.
  - **Forward-auth + custom login / SSO** (Traefik `forwardAuth` → a skali auth
    service) — per-user access, sessions, audit. More power, much more work; add
    only when basic-auth isn't enough.
  - **Signed preview links** — a tokenised URL for time-boxed share access.
- Applied at whichever proxy tier terminates the request (edge for remote apps,
  node proxy otherwise) — no app code changes.

## Container network isolation (defense in depth)

The daemon owns Docker on each node and configures networking so a rogue
container cannot pivot to the control plane or the rest of the cluster.

- **Network-per-app + shared proxy network.** Each Project gets its own internal
  Docker bridge network for its containers; the web container is additionally
  attached to a `skali-proxy` network shared only with the node proxy. Apps
  cannot see each other's containers.
- **Non-optional firewalling (the critical part).** The daemon installs rules in
  Docker's **`DOCKER-USER` iptables chain** (the supported hook that survives
  Docker's own rule management) to **drop traffic from container subnets to**:
  - the host's control-plane ports (gRPC, REST) and Docker socket,
  - the private cluster network / other nodes' management surfaces.
  This holds even if a container is fully compromised; and if the firewall ever
  failed, control-plane mTLS still rejects the connection (layered).
- **Egress policy (per-environment, default permissive).**
  - `internet` *(default)*: may reach the internet; **may not** reach the host
    control plane or cluster management network.
  - `isolated`: a Docker `internal: true` network — no egress at all; reachable
    only by the node proxy. For locked-down workloads.
  - The container→control-plane block is **always on**, in every mode.

### East-west connectivity: app ↔ database (binding-gated)

App→database traffic is **east-west** and unlike everything above it does **not**
go through Traefik — it's **raw TCP** (the Postgres wire protocol), not HTTP, so
Traefik (ingress/L7 only) never sees it.

- **Default is deny.** Network-per-app isolation means an app cannot reach a
  shared database pool any more than it can reach a sibling app's containers.
- **A binding opens exactly one path.** When app *X* binds a database on pool *P*
  (`17`), the daemon grants *X* reachability to *P*'s listener only — by attaching
  *X*'s web container to *P*'s data network (or opening *P*'s port to *X*'s
  subnet). An app with no binding to *P* still can't reach it, and the
  always-on container→control-plane block is unaffected.
- **Shared pools stay isolated per tenant.** Many apps may reach one shared pool
  (each via its own binding), but Postgres-level role isolation (`08`/`17`) keeps
  each logical database private — network reachability ≠ data access.

So the dense "20 apps, one Postgres server" topology coexists with strict default
isolation: connectivity is **granted per binding, never ambient.**

## Default ports (configurable)

| Purpose | Default | Exposure |
|---------|---------|----------|
| REST API | `:7000` | private (or behind edge) |
| gRPC control plane | `:7001` | private, mTLS |
| Node proxy entrypoint | `:7080` | private |
| Edge proxy | `:80` / `:443` | **public** |
| Built-in registry | `:5000` | private |
| Database pool (Postgres) | `:5432` (per pool) | private, **binding-gated** |

## Resilience notes

- **Edge survives master outage (mostly).** Traefik caches its last good config;
  if the master's HTTP provider is briefly unavailable, existing routes keep
  serving. New routes/changes pause until the master returns. Running apps are
  unaffected. (Master HA is `[future]` — see `16`.)
- **Single edge is a bottleneck/SPOF** for now. Because edge config is centrally
  generated and the proxies are stateless, scaling out is "run more `edge` nodes
  polling the same HTTP provider, front them with DNS round-robin / a floating
  IP / an L4 LB" — no redesign. Cloud LBs are an optional *front* layer, never
  the core mechanism.
