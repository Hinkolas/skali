# Fleet and add-ons proposal

Unscheduled design proposal, preserved from [issue #93](https://github.com/Hinkolas/skali/issues/93)
when separating roadmap directions from implementable work. This is not an
implementation contract; illustrative APIs and manifest fields are proposals,
not supported interfaces. The issue retains the original discussion history.

This proposal is for later. It records a design discussion so I can come back to it; nothing here is scheduled.

Two needs have the same shape:

- **Shared capabilities used by many projects.** Email, mobile push, background removal, image and video processing. Today each project reimplements them, with its own provider credentials, retries, rate limits, templates and delivery logs.
- **Compute outside the cluster.** Idle machines such as Mac minis, a Mac Studio and desktops sit behind home or office NAT with changing IPs. They could serve latency-sensitive inference (warm models, no cold starts), managed builds (ROADMAP: "Push-to-deploy … a managed builder") and CI runners.

skali already solves "host it once, give each project scoped access" for Postgres and S3. This proposal generalizes that with two pieces:

1. A **built-in fleet substrate** for queued jobs and external workers.
2. **Cluster-wide add-ons** that projects attach to.

## What exists

- **Shared substrates.** `databases:` and `buckets:` run on shared substrates (CNPG, SeaweedFS). Each project gets its own claims, with credentials injected through `{{databases.k.output}}` and `{{buckets.k.output}}`.
- **Identity and isolation.** Environment namespaces carry the `skali.dev/*` labels. Membership already exists. API/CI tokens are on the roadmap. Credential rotation is #78 and network isolation is #89.
- **Rollouts and edge.** Blue-green rollouts keep the previous color for a fixed 15 s retire drain (`defaultRetireDrain` in `internal/reconcile/bluegreen.go`), behind the Traefik edge.
- **A standalone worker platform** that I'm building separately, designed to run on skali. It has a RunPod-compatible job API, a Go agent per machine with an outbound WebSocket connection, a Postgres queue with leases, and optional spill to RunPod. It would be the basis for the substrate below.

## Proposal

### 1. Fleet substrate (built in, optional)

The fleet would sit at the same layer as CNPG behind `databases:`. It ships and upgrades with skali, is off until enabled, and runs as its own component, not inside skalid.

- **Workers.** A cluster admin enrolls each machine. The machine runs a single agent binary that connects outbound over WSS to the fleet gateway route. It needs no inbound access, static IP or VPN.
- **Queue store.** Postgres, claiming with `FOR UPDATE SKIP LOCKED`. Leases carry fencing tokens, delivery is at-least-once, and wakeups use `LISTEN/NOTIFY`.
- **One scheduler for every consumer.**
  - Each queue declares a priority class (`interactive` or `batch`) and what its jobs need: CPU, memory, and exclusive hardware such as the GPU or media engine.
  - Agents report free capacity, and a job is dispatched only where it fits.
  - Batch work is not admitted while interactive work is waiting. Running jobs are never pre-empted.
- **How jobs run on the agent.** Either a warm pool of long-lived handler processes that keep their models loaded, or a fresh VM per job (Tart on Apple Silicon) for builds and CI.
- **Spill backends.** Optional per queue, for example forwarding to a RunPod endpoint when the fleet is busy or offline.
- **Client API.** RunPod-compatible: `/run`, `/runsync`, `/status`, `/cancel`, `/health`.

### 2. Manifest surface: `queues:`

Illustrative only, not validated grammar:

```yaml
queues:
  cutout:
    class: interactive
environment:
  JOBS_URL: "{{queues.cutout.url}}"
  JOBS_TOKEN: "{{queues.cutout.token}}"
```

The project that owns a queue also owns its worker pools: the handler command and code, the number of processes, and their resource needs. The machines belong to the cluster.

### 3. Add-ons

These follow the Heroku add-on model, installed once per cluster:

- **Install and visibility.** Add-ons are installed from a catalog. In the web console each gets its own sidebar entry next to Projects. In the CLI they are managed with `skali addon …`.
- **Under the hood.** An add-on is deployed like a project in its own namespace, reusing rollout, scaling, databases and buckets. It is not presented as a project.
- **Provider contract.** An add-on declares what it provides and a bind hook.
  1. On attach, skali calls the hook with the caller's project and environment identity.
  2. The add-on provisions a tenant and returns outputs.
  3. skali stores those outputs as secrets and injects them as `{{addons.<name>.<output>}}`.

  Detaching, or deleting the project, calls the hook again so the add-on cleans up.
- **Identity.** A per-binding credential or a skali-signed workload identity. This builds on API tokens and #78.
- **Network.** Only attached environments can reach an add-on. This builds on #89.
- **Same interface as projects.** Add-ons use the same `queues:` surface as projects, with no privileged path. Many add-ons, email for example, don't need the fleet at all.

### 4. Ownership

| Scope | Owns |
|---|---|
| Cluster (admin) | Worker machines, priority-class policy, per-project capacity quotas |
| Project or add-on | Its queues, its worker pools, its jobs |
| skali itself | Reserved system queues such as `skali/builds` |

A project requests a priority class, and admin policy grants a maximum. Otherwise any project could mark itself `interactive` and starve the others.

### 5. System use and the recovery rule

- **Managed builds.** A managed builder becomes a skali build backend on the reserved `skali/builds` queue. Each build runs BuildKit in a fresh VM per job, and builds of the same project are routed to the same machine so its layer cache stays warm.
- **Fallback.** Local CLI builds remain a permanent fallback.
- **Recovery rule.** skali never needs the fleet or any add-on to recover itself. Deploys keep working while the fleet is down.

## Suggested order

1. API/CI tokens, which provide the identity subjects.
2. Network isolation by default (#89).
3. The fleet substrate and `queues:`, ported from the standalone worker platform.
4. The managed builder backend on `skali/builds`.
5. The add-on contract: provider declaration, bind hooks, catalog, sidebar and CLI.
6. Reference add-ons for email and push.

## Open questions

- **Long-lived worker connections under blue-green.** Options are a per-application drain window (today the drain is the kernel-wide 15 s `defaultRetireDrain`), per-route edge timeouts, or active handoff on SIGTERM, which works today.
- **Worker enrollment and agent updates.** How enrollment and updates work, how the agent runs under macOS launchd, and whether Metal is available to a launchd service.
- **Priority-class policy and quotas.** How the admin defines them and how they are enforced.
- **Add-on packaging and versioning.** Contract versions, deprecation windows, and upgrading an add-on without breaking its consumers.
- **Multi-tenancy inside add-ons.** Data isolation, quotas and cleanup are the add-on's job; skali only provides identity.
- **Blast radius and privilege.** Add-ons hold secrets for many projects (APNs keys, for example), which makes them the most privileged workloads on the cluster.
- **Fleet database.** Whether the fleet gets a dedicated Postgres pool or uses the platform's own.
- **Naming.** Add-ons or services/bindings, and fleet.

## Acceptance criteria

These apply once the work is picked up.

- [ ] The fleet substrate can be enabled per cluster and is disabled by default. Deploys and recovery are unaffected while it is down.
- [ ] A machine behind NAT enrolls with a token and serves a project's queue without any inbound connectivity.
- [ ] `queues:` validates in the manifest and its outputs are injected. Jobs from two projects share the same workers and are scheduled by priority class.
- [ ] Managed builds run through `skali/builds`, with local CLI builds as the fallback.
- [ ] The add-on contract covers installing from the catalog and attaching to and detaching from a project, with injected outputs and cleanup. Add-ons appear in the web sidebar and the CLI.
- [ ] One reference add-on (email) is attached to two projects.

Related: #78 (credential rotation), #89 (network isolation), and ROADMAP items "Push-to-deploy", "API/CI tokens" and "More database engines (Valkey/Redis first)".
