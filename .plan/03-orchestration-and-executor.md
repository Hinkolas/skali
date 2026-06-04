# 03 — Orchestration & the Executor Seam

## The Executor interface (the one multi-node seam)

The service layer never touches Docker. It calls an `Executor`, which represents
"the ability to act on a node." There are exactly two implementations:

- `LocalExecutor` — talks to the local Docker daemon via the official Go SDK.
- `RemoteExecutor` — a gRPC client to another node's `skalid`.

The remote node's gRPC server is a *thin wrapper that calls its own
`LocalExecutor`*. So the Docker-touching code is written once and runs at both
ends. "One coherent command structure" is achieved at this interface — **not** by
forcing the local node through gRPC.

```go
// Transport-neutral so Local and Remote share one signature.
// Streaming methods return channels/readers, not gRPC-specific types.
type Executor interface {
    EnsureNetworks(ctx context.Context, spec NetworkSpec) error
    PullImage(ctx context.Context, ref string, creds RegistryAuth) (<-chan Progress, error)
    BuildImage(ctx context.Context, req BuildRequest) (<-chan LogLine, error) // builder role
    RunContainer(ctx context.Context, spec ContainerSpec) (InstanceID, error)
    StopContainer(ctx context.Context, id InstanceID, timeout time.Duration) error
    RemoveContainer(ctx context.Context, id InstanceID) error
    InspectContainer(ctx context.Context, id InstanceID) (InstanceStatus, error)
    StreamLogs(ctx context.Context, id InstanceID, opts LogOptions) (<-chan LogLine, error)
    WatchEvents(ctx context.Context) (<-chan NodeEvent, error) // docker events → reconciler
}
```

### Resolving an executor (the short-circuit)

```go
func (o *Orchestrator) executorFor(node *Node) Executor {
    if node.ID == o.selfNodeID {
        return o.local            // direct Docker calls; no loopback gRPC
    }
    return o.remoteFor(node)      // gRPC client (mTLS) to node.advertiseAddr
}
```

The short-circuit is *less* work than always-gRPC (no localhost mTLS dial, no
self-serialization of streams) and uses the `LocalExecutor` that must exist
anyway. Streaming stays uniform because the interface returns channels;
`LocalExecutor` fills them from Docker, `RemoteExecutor` from the gRPC stream.

## Desired state vs actual state — the reconciliation loop

> **New design decision surfaced while writing this down.** Earlier discussion
> implied imperative commands ("master tells node to run X"). That is fragile:
> what happens when a node reboots, a container OOM-crashes, or the daemon
> restarts mid-deploy? We adopt a **declarative reconciler** (a small
> controller-pattern loop), which is the right backbone for a multi-node system.

- **Desired state** lives in SQLite on the master: the set of Releases that
  should be running and, for each, how many Instances on which nodes.
- **Actual state** is the set of containers actually running on each node.
- The **reconciler** continuously drives actual → desired:
  1. For each node, compute the desired Instance set (from Releases) and diff it
     against the node's reported actual set.
  2. Issue the minimal `Executor` calls to converge: pull image, run missing
     containers, stop/remove extra ones.
  3. Workers report actual state via **`WatchEvents`** (Docker events) plus
     periodic heartbeat/inventory; the master reconciles on change and on a
     timer.
- **Crash recovery on a live node:** if a container dies, the Docker event fires,
  actual diverges from desired, the reconciler re-runs it. This gives
  self-healing for container crashes *for free* — without any extra "restart
  policy" plumbing.
- **Node reboot / daemon restart:** on reconnect, the worker reports its (likely
  empty) actual set; the reconciler re-creates the desired Instances.
- **Dead-node rescheduling** (moving Instances to a *different* node when one
  goes away) is **`[soon]/[future]`**, not 0.1.0 — it needs failure detection +
  placement policy and risks split-brain. 0.1.0 reconciles within live nodes
  only.

### Robustness principles (the reconciler must be boring and safe)

The reconciler is the heart of the runtime, so it is built to be dull and
predictable:

- **Idempotent steps.** Every converge action is safe to repeat; re-running with
  the same desired+actual produces no change. Re-runs on reconnect or restart are
  normal, not exceptional.
- **Containers are self-describing.** Each carries `skali.project`,
  `skali.environment`, `skali.release`, `skali.instance` labels, so after any
  daemon/node restart the worker rebuilds "what is running and what it belongs to"
  purely from Docker, then reconciles — **no local state needed**.
- **Startup resync is first-class.** On `skalid start`, a worker reports its
  actual inventory and the master converges it to desired. This is the *same*
  code path as steady-state reconcile, not a special boot mode — exactly the
  "node starts, gets its desired state, reproduces it" flow.
- **Level-triggered, not edge-triggered.** Convergence acts on the *current*
  diff, not a queue of past events, so a missed Docker event self-heals on the
  next pass. Events and heartbeats make convergence prompt; they are not
  correctness requirements.
- **Single writer.** Only the master mutates desired state; workers only act and
  report. No split-brain within a single-master cluster.

### Deploy as a desired-state change

A deploy does not "run a container" imperatively. It:
1. Produces a new Release for the target **Environment** (image digest + that env's
   config snapshot) — see `04`. A `promote` produces the same kind of Release but
   reuses another environment's digest instead of building.
2. Marks it the desired version for that Environment.
3. The reconciler converges nodes to it, using the rollout strategy below. Each
   environment converges independently.

## Rollout / zero-downtime strategy

> **Another gap surfaced while writing.** We never discussed how a redeploy swaps
> versions without dropping requests. Define it now.

- **0.1.0: start-new-then-stop-old (per Instance), health-gated when possible.**
  1. Reconciler starts the new Instance(s) for the new Release alongside the old.
  2. **If the Release declares a health check** (Docker `HEALTHCHECK` and/or a
     configured HTTP readiness probe), it waits for the new container to pass it
     before considering the Instance live.
  3. **If no health check is declared, the Instance is treated as healthy the
     moment it has started** and the reconciler proceeds immediately. *(This is
     the chosen default: no probe ⇒ assume healthy ⇒ stop the old one as soon as
     the new one is up.)*
  4. The node proxy (Traefik) routes to backends as they become live, so traffic
     shifts to new Instances.
  5. Once the new Release is live at the desired replica count, the reconciler
     stops/removes the old Instances.
  6. **Safety net (only with a health check):** if the new Instances never become
     healthy within a timeout, the deploy is marked **failed** and the old
     Release keeps serving. **Without** a health check this net does not exist —
     the old Instance is stopped as soon as the new one starts, so a new image
     that starts-then-crashes can cause a brief outage. This is the documented
     tradeoff of omitting a health check; declaring one restores the guarantee.
- **Rollback** is just "set desired version back to a prior Release" — the same
  machinery, in reverse. Because Releases pin an image **digest**, rollback is
  deterministic. First-class even in 0.1.0.
- **[soon]** richer strategies (blue/green with explicit cutover, canary
  percentages) layer on top of the same reconciler.

## Docker integration specifics

- Always the **official Go SDK** (`github.com/docker/docker/client`), never
  shelling out to `docker`/`docker compose`.
- The daemon connects to the **local** Docker socket on its own host; it never
  talks to a remote Docker socket (cross-node action goes through gRPC, not
  remote Docker APIs — safer and uniform).
- **Image presence:** before running, ensure the image digest is present
  (`PullImage` from the registry). On the build node the image may already be
  local; pull is then a no-op/fast (see `04` for the always-through-registry
  decision).
- **Labels:** every app container is created with the Traefik labels needed for
  the node proxy to route to it (host rule, internal service port, health check,
  load-balancer group), plus skali bookkeeping labels (`skali.project`,
  `skali.environment`, `skali.release`, `skali.instance`) so the reconciler can map
  containers back to desired state and recover after a daemon restart.
- **Resource limits** (`[soon]`): CPU/memory limits (and proxy upload-size limits)
  from the **Environment** config map to Docker host-config / Traefik constraints —
  so e.g. production can grant more RAM and larger uploads than staging.
- **Build is a second, optional driver.** `Executor.BuildImage` runs a build on a
  **builder node**. The *same* BuildKit build-and-push routine (shared
  `internal/builder`) can also run **client-side in the `skali` CLI** (local build,
  `04`) — that path does **not** go through an `Executor`; the CLI builds, pushes a
  digest, and the master only does the release/reconcile half. The reconciler is
  unchanged either way: it converges on a digest, indifferent to where it was built.
- **Builds use a separate job queue, not this reconciler.** Server-side builds are
  scheduled through the `builds` table as run-once jobs (master = scheduler, gRPC
  dispatch; `04`). Keep the two loops distinct: **builds = run-once tasks** (retry
  on failure), **instances = level-triggered desired state** (converge forever).
- **Placement is architecture-aware.** Every node reports its arch
  (`nodes.arch`, `07`). The reconciler only places a Release's Instances on nodes
  whose arch matches the image's `target_platform`; in 0.1.0 (native-only builds)
  an arch mismatch is reported as an error rather than scheduling an unrunnable
  container. (Emulated cross-arch is `[future]`, `04`.)

## Multi-service apps (Compose)

0.1.0 deploys a **single service** built from a `Dockerfile`. Real apps often
need a DB/cache/etc. **[soon]:** support a committed `compose.yaml` as the
Project's service definition. We parse it with `compose-go` and translate
services into skali Instances + labels — still driving the engine via the Docker
SDK, never the compose CLI. The data model (`05`) already allows a Project to
own multiple services to make this non-breaking.
