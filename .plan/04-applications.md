# 04 — Application services

An application = a container image the user brings, run with replicas, env,
resources, optional persistent volumes, and optional domains. Git-based builds are
explicitly **later**; v1 is image-based (this is also what keeps the registry story
trivial).

## Spec (v1 fields, `service_applications`)

| Field | Notes |
| --- | --- |
| `image` | Tag reference, any registry reachable from the cluster. |
| `port` | Single container port v1; Service/Ingress target. |
| `replicas` | ≥1. Forced to 1 when volumes exist (see Volumes). |
| `cpu_millis`, `memory_mb` | Pod resources; requests = limits v1 (simple, predictable). |
| `env` | Plain name/value pairs. Values from connections are injected on top (below). Secret-typed env vars: later. |
| `health_path` | Optional HTTP path → readiness + liveness probes; also gates rollouts. |
| `domains` | Hostnames (+ optional path) → Ingress rules. |
| `volumes` | name, mount path, size → PVC (local-path, RWO). |

## Deploy flow (the core UX moment)

1. User hits Deploy (create, or "deploy new image/config").
2. API resolves `image` tag → digest via go-containerregistry (remote HEAD;
   error surfaces immediately as today's registry errors do).
3. Insert `releases` row: digest pin + full spec snapshot, status `pending`.
4. Controller compiles the release → SSA-applies:
   - **Deployment** — pod template labeled with `skali.dev/release`; image pinned
     `repo@digest`; strategy `RollingUpdate` (`maxUnavailable: 0, maxSurge: 1`) when
     stateless, `Recreate` when volumes exist; probes from `health_path`.
   - **PVCs** — one per volume entry, `local-path` storage class.
   - **Service** — ClusterIP on `port`. Internal DNS falls out:
     `<slug>.prj-<project>.svc.cluster.local` — the same shape Sevalla shows.
   - **Ingress** — one rule per domain via bundled Traefik. TLS: M4 (cert-manager);
     until then plain HTTP or wildcard certs by hand.
5. Controller watches the Deployment: available at new generation → release
   `active`, previous `superseded`, service `running`; progress-deadline exceeded →
   release `failed`, service `degraded` + `status_detail`.

**Rollback** = pick an old release → new release row cloned from its snapshot →
same flow. Nothing special-cased.

## Volumes — the honest v1 constraint

`local-path` PVCs are node-pinned and RWO. Therefore: an app with volumes runs
`replicas: 1` with `Recreate` strategy, and the UI says why ("persistent volumes pin
an app to one node and one instance"). This is the same constraint every homelab
platform has; we state it instead of hiding it. Multi-replica stateful apps and
RWX shared volumes (Longhorn/NFS) are Later. Kubernetes handles the actual pinning
(volume node affinity) — skali writes zero placement code.

## Connections (app → database)

A `connections` row injects env into the app's pods at compile time, sourced from
the database service (05):

```
<PREFIX>_HOST      <pool>-rw.skali-dbs.svc.cluster.local
<PREFIX>_PORT      5432
<PREFIX>_NAME      <db_name>
<PREFIX>_USER      <db_user>
<PREFIX>_PASSWORD  (valueFrom secretKeyRef — never materialized into skali's DB)
<PREFIX>_URL       assembled in the container env (postgres://…)
```

Creating/removing a connection bumps a new release (config change = release, always —
one mental model). Cross-namespace secret reads are avoided by CNPG's secret living
in `skali-dbs`: skali mirrors the secret into the project namespace as an owned
object (`skali.dev/managed`), which the prune loop keeps in sync.

## Logs & metrics

- **Logs** (v1): API proxies pod logs (`follow` streaming) → simple logs tab.
  Aggregation/retention: later.
- **Metrics** (v1): metrics-server pod CPU/memory for the overview tab, same
  presentation as the mock (`StatCard` + existing `TimeSeriesChart`). History: later
  (small poller into a `service_metrics` table, pattern reusable from the old
  `node_metrics` — only if the UI needs it in M4).

## Later (kept out deliberately)

Sidecars + cron jobs (the Nextcloud driver — same pod / CronJob compilation, needs a
`processes` list on the spec), secret-typed env vars, autoscaling (HPA is cheap once
wanted), git builds (buildpacks/Nixpacks + in-cluster registry — this is when a
registry component returns), per-app egress/network policies, custom health
commands, multi-port services.
