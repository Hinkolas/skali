# 02 — Architecture

> [!WARNING]
> Historical first-rework document, superseded by
> [`REWORK_V2.md`](../REWORK_V2.md). Retained for context; this topology and
> package plan are not the current V2 contract.

## Components

```
┌─────────────────────────────────────────────────────────────┐
│ k3s cluster                                                 │
│                                                             │
│  bundled: containerd, flannel, CoreDNS, Traefik,            │
│           local-path-provisioner, metrics-server, Spegel    │
│  blessed operators (pinned, installed by skali bootstrap):  │
│           CloudNativePG, cert-manager (from M4)             │
│                                                             │
│  ┌─────────────┐   ┌──────────────┐   ┌──────────────────┐ │
│  │ skalid      │──▶│ kube-api     │──▶│ workloads:       │ │
│  │ API+control │   │ (SSA apply,  │   │ prj-* namespaces │ │
│  │ + web (BFF) │◀──│  informers)  │   │ skali-dbs pools  │ │
│  └─────┬───────┘   └──────────────┘   └──────────────────┘ │
│        │ intent                                             │
│  ┌─────▼───────┐                                            │
│  │ Postgres    │  (skali state: users, projects, services) │
│  └─────────────┘                                            │
└─────────────────────────────────────────────────────────────┘
   ▲                ▲
   │ HTTPS          │ skali CLI (talks skali API, never kubectl)
  browser          terminal
```

- **`skalid`** — one binary, one mode (`serve`): chi REST API + the controller.
  The `agent` mode is gone; nodes run k3s only.
- **Postgres** — skali's own state. Dev: external/docker (as today, `DATABASE_URL`).
  Prod: external, or in-cluster via CNPG once databases land (M3+); self-hosting
  skali's own DB on its own primitive is the dogfood path, not a v1 requirement.
- **web/** — SvelteKit BFF, unchanged model (httpOnly session cookie, `/api/[...path]`
  proxy, server-side `apiFetch`).
- **CLI** — talks the skali API with a token. kubectl remains available to admins as
  the escape hatch, but skali never requires it for product flows.

## The control loop (the one piece of "orchestration" skali keeps)

Same philosophy as the m5 reconciler — level-triggered, nothing needed to resume
lives in memory — with a body that is ~10× smaller because k8s does the hard half:

1. **Compile**: pure function per service type: `(service row + related rows) →
   []manifest` (typed structs or unstructured). No I/O inside compilers.
2. **Apply**: server-side apply of every compiled manifest with
   `fieldManager: skali`, `force: true`. SSA makes applies idempotent and diffs
   irrelevant — no hand-rolled config-hash comparison (the config-hash idea survives
   only as a label for cheap "did intent change" checks if we ever need it).
3. **Prune**: objects labeled `skali.dev/managed=true` whose owning service/release
   no longer wants them are deleted. Ownership via labels, exactly like the old
   ownership model — the idea transfers 1:1.
4. **Observe**: shared informers on the object kinds we own (Deployments, Pods, CNPG
   Clusters/Databases, Ingresses, PVCs, Nodes). Events → coalescing poke per service
   (the doorbell pattern survives, minus gRPC). Periodic full pass as backstop.
5. **Status**: derive a per-service rollup from watched conditions
   (Deployment availability, CNPG Cluster phase) and cache it on the service row —
   same "computed, never authoritative" stance as the old `rollupStatus`.

Concurrency: per-service worker with an in-flight guard (port of the existing
acquire/release pattern). Retries/backoff on apply errors, stored on the service row
(`last_error`, `next_attempt_at`) — the m5 error surface conventions carry over.

## New/changed packages

```
internal/
  kube/        client construction (kubeconfig | in-cluster), SSA apply helper,
               informer factory, typed getters, label schema
  compile/     per-type compilers: application.go, database.go, pool.go, shared naming
  services/    domain layer: projects + services CRUD, validation, releases,
               connections (app↔db), password/credential generation
  controller/  the loop above: pass, pokes, status rollup, prune
```

Everything else (`auth`, `api`, `store`, `obs`, `crypt`, `config`, `cliconfig`,
`client`, `testdb`, `version`) stays where it is. Dependency to add:
`k8s.io/client-go` (+ `k8s.io/api`, `apimachinery`) and keep
`google/go-containerregistry` for tag→digest resolution. We deliberately do **not**
use controller-runtime: our desired state lives in Postgres, not CRDs, so
client-go + informers is the honest fit.

## Labels (replaces `internal/engine/labels.go`)

```
skali.dev/managed:  "true"                    on every object skali applies
skali.dev/project:  <project slug>
skali.dev/service:  <service slug>
skali.dev/release:  <release id>              application pods only
skali.dev/pool:     <pool name>               database pool objects only
app.kubernetes.io/managed-by: skali
```

Namespace scheme: one namespace per project (`prj-<slug>`), shared database pools in
`skali-dbs`, skali's own components in `skali-system`.

## Bootstrap & node management

- **Cluster init** (server node): install k3s pinned to a channel
  (`curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL=... sh -` or documented
  config file at `/etc/rancher/k3s/config.yaml`). Keep bundled Traefik/CoreDNS/
  local-path/metrics-server — they are the point of k3s.
- **skali install**: a `deploy/` kustomization (or `skali install` CLI command later)
  that applies, in order: pinned CNPG operator manifests → `skali-system` namespace →
  skalid Deployment + Service + Ingress (+ Postgres, per above). Idempotent; also the
  upgrade path.
- **Add node**: k3s agent join — UI/CLI shows the join command with the cluster
  token (`K3S_URL=https://<server>:6443 K3S_TOKEN=<token>`), mirroring the old
  enroll-command modal UX. skali reads node inventory/health via the kube API
  (`nodes` page becomes a read of `v1.Node` + metrics-server).
- **Images**: enable k3s embedded registry mirror (Spegel) for P2P distribution;
  `registries.yaml` only if private upstream registries appear later. skali resolves
  tags to digests at deploy time and pins pod specs by digest — the m4 *policy*
  without the m4 infrastructure.

### Node health (no skali agent, ever)

Nodes run only the k3s agent (kubelet + containerd + flannel). skalid is a single
instance talking to the kube-apiserver. Health/facts sources, all via that one API:

- **`v1.Node` objects** — kubelet lease renewal ~10s is the heartbeat (old
  `last_seen`); control plane flips `Ready→Unknown` after ~40s of silence (old
  online/offline). Conditions (`Ready`, `MemoryPressure`, `DiskPressure`,
  `PIDPressure`), capacity/allocatable, addresses, `nodeInfo` (OS/arch/kernel/
  k3s version/runtime) replace the old node facts. Watched by the existing informer.
- **metrics-server** (bundled) — live CPU/memory per node and per pod
  (`metrics.k8s.io`) for the nodes page and analytics.
- **kubelet stats summary** (`/api/v1/nodes/{node}/proxy/stats/summary`) — node/image
  filesystem and PVC usage when real disk numbers are needed.
- **History** — M4: small poller persists metrics-server readings to Postgres
  (old `node_metrics` pattern, collection side now k8s's problem).

Known gap vs old `hostinfo`: no load average / network / disk-I/O rates from
metrics-server. Deferred; node-exporter + Prometheus is the later path if wanted.

## macOS support (recorded constraint)

Kubernetes nodes are **Linux-only** (kubelet/containerd need the Linux kernel; there
is no macOS node type). This is not a regression: Docker — the old substrate — has no
macOS backend either (Docker Desktop = hidden Linux VM), so skali's data plane was
always Linux. What works on a Mac:

- **Development**: fully — k3d inside Docker Desktop, `skalid` + web dev server
  native (pure Go / Node). See below.
- **Mac fleet as the cluster** (a first-class target: Nicholas runs 17 Mac minis,
  **all M4** — which rules out Asahi Linux (M1/M2 only), so the fleet shape is:
  - **One headless Linux VM per Mac**: Virtualization.framework via Lima
    (`limactl` + launchd autostart), **bridged networking** so each VM has its own
    LAN IP, most RAM/CPU allocated; k3s runs directly inside the VM.
    Near-native CPU; costs are fixed memory reservation, VM disk images, and the
    ops layer of keeping VMs alive across macOS updates.
  - Fleet topology: 3 k3s servers with embedded etcd (HA), 14 agents, flannel
    across the VM LAN IPs, edge node(s) run Traefik.
  - **Thinnest-stack rule**: on macOS a Linux VM is non-negotiable (containers are
    a Linux kernel feature; Docker on macOS is itself a hidden VM — the old Docker
    substrate had the exact same constraint). The only choice is stack thickness:
    fleet = VM → k3s (thin, bridged, headless); dev = Docker Desktop's existing
    VM → k3d (thick but disposable and already there). Do NOT run the fleet as
    k3d-in-Docker per Mac: it adds dockerd + a container layer for nothing, forces
    flannel through per-VM NAT with published ports, and ties nodes to desktop
    software (Docker Desktop: GUI session, licensing; Colima is Lima anyway).
- **Control plane only on a Mac**: `skalid` runs natively anywhere Go runs and only
  needs kube API + Postgres; possible, though in-cluster is the recommended topology.

## Dev environment (macOS, Docker Desktop)

- **k3d** (k3s-in-docker) provides the dev cluster:
  `k3d cluster create skali-dev -p "8080:80@loadbalancer"` — Traefik reachable on
  localhost, kubeconfig merged automatically. Managed via the global Taskfile
  toolchain (go-task, per existing convention; no per-project compose).
- `skalid serve` runs on the host against `KUBECONFIG` + local Postgres — dev loop,
  ports (7070 API / 5173 web), and `task dev` shape stay as they are today.
- CNPG operator installed into the dev cluster once per cluster create (task target).
- Tests: unit tests for compilers are pure (manifest golden tests — biggest testing
  win of the pivot); store tests keep `internal/testdb`; controller integration tests
  against k3d (or envtest later if the k3d loop is too slow).

## Config (shrinks)

Gone: `CLUSTER_ADDR`, mirror/registry settings, agent settings, data-dir identity.
Remains/new: `DATABASE_URL`, `AUTH_SECRET`, listen addr, `KUBECONFIG` override
(empty = in-cluster), blessed image/versions table (CNPG image catalog, default
Postgres major).
