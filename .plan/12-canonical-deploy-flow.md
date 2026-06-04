# 12 — Canonical Deploy Flow (end to end)

This ties every component together: the exact sequence behind `skali auth login`
then `skali deploy` for a brand-new project on a single-node cluster (node A holds
all roles). This is the 0.1.0 acceptance scenario.

## Preconditions

- `skalid start` is running on node A (master+edge+worker+builder) — which also
  brought up the managed `registry:2` container (the default `builtin` Registry).
- A wildcard DNS record `*.apps.example.com` → node A's public IP.
- The developer has a project dir with a `Dockerfile`, no `.skali/` yet.

## Step 0 — login (once)

```
$ skali auth login --master https://skali.example.com
  → POST /api/v1/auth/login {email, password}
  → master verifies (argon2id), mints opaque token, stores hash in `tokens`
  → CLI stores token in ~/.config/skali/config.yaml (current context)
```

## Step 1 — `skali deploy` with no config → wizard

```
$ skali deploy
  no .skali/ found → runs the shared init wizard (same flow as `skali init`),
  configuring the default `production` environment:
    name      = my-website
    domain    = my-website.apps.example.com
    node      = node-a            (only worker)
    replicas  = 1
    dockerfile= ./Dockerfile
  → POST /api/v1/projects {name, registry=builtin, build…}
       (server also creates the default `production` environment)
  → POST /api/v1/projects/{id}/routes?env=production {host: my-website.apps.example.com, port:3000}
  → master returns project id (prj_…)
  → CLI writes .skali/config.yaml (with project.id) + .gitignore entry
```

## Step 2 — upload context & build

```
  CLI tars build context (honoring .dockerignore)
  → POST /api/v1/projects/{id}/deploys?env=production   (multipart: context.tar)
  → master persists context blob + records Build(status=queued),
    returns {deploy_id, build_id}
  (deploy is ASYNC by default: it would print the job id and return here;
   this walkthrough passes `--follow`, so the CLI attaches the stream)
  CLI opens SSE: GET /api/v1/deploys/{deploy_id}/logs

  master → Executor.BuildImage(local, in 0.1.0):
     docker build (BuildKit) from uploaded context
     tag = <builtin-registry>/my-website:<build_id>
     docker push → built-in registry (loopback on node-a)
     resolve pushed digest  (sha256:…)
  build logs stream: builder → master → client SSE (live in terminal)
  master records Build(image_digest, status=succeeded)
```

## Step 3 — release & reconcile (health-gated rollout)

```
  master creates Release(environment=production, image_digest, env_snapshot,
                         replicas=1, placement) and marks is_current=1  (desired)
  reconciler diffs desired vs actual for node-a (production env):
     Executor.EnsureNetworks (per-app net + skali-proxy net)
     Executor.PullImage(digest)            # already local → fast
     Executor.RunContainer(spec with Traefik labels:
         Host(`my-website.apps.example.com`), service port 3000,
         healthcheck, skali.project/environment/release/instance labels,
         networks, egress=internet firewalling)
     wait for container HEALTHY (health-gate)
  node-a Traefik (Docker provider) discovers the container via labels,
     begins routing the host to it
  reconciler records Instance(status=healthy)
```

## Step 4 — serve

```
  user → https://my-website.apps.example.com
       → edge Traefik on node-a (TLS via ACME HTTP-01, terminates)
       → (edge==worker, no hop) routes via Docker labels → container :3000
       → response
```

## Step 5 — finish

```
  master marks deploy succeeded
  CLI SSE shows rollout complete, prints:
     ✓ my-website is live at https://my-website.apps.example.com
       release rel_…  on node-a
  .skali/config.yaml already holds project.id → directory is linked
```

## Subsequent deploys

`skali deploy` now finds `.skali/config.yaml`, skips the wizard, uploads context,
builds a new image digest, creates a **new Release**, and the reconciler does a
**start-new-then-stop-old** health-gated swap (`03`) — zero-downtime. A bad build
leaves the previous Release serving.

## Multi-node variation (when node B is added later)

Only steps 3–4 change:

- Release placement targets node-b → reconciler calls **`RemoteExecutor`**
  (gRPC, mTLS) to pull+run on node-b.
- The master's edge HTTP-provider config now includes a `host → node-b` entry
  (remote-app), so edge Traefik on node-a forwards over the private network to
  node-b's proxy, which routes to the container. One extra hop; everything else
  identical.

## Environments variation

The flow above runs entirely in the implicit default `production` environment.
Adding a second environment is **config-driven and in 0.1.0**:

- `skali env create staging` edits `config.yaml` (a `staging` block:
  `staging.example.com`, `.env.staging`, maybe fewer replicas) — a local change.
- `skali apply` (or `skali deploy --env staging`) **reconciles** that env onto the
  server: creates it, adds its route, sets resources/placement. `skali env vars
  push --env staging` syncs `.env.staging`.
- `skali deploy --env staging` builds and ships to staging only; production keeps
  serving its current release untouched.
- **[soon]** after verifying, `skali promote staging production` releases
  **staging's exact digest** into production with production's own
  vars/routes/resources — **no rebuild** — advancing production by all the commits
  validated on staging.

This is the whole system exercised end to end. If steps 0–5 work on one node,
0.1.0 is met.
