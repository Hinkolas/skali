# 12 — Canonical Deploy Flow (end to end)

This ties every component together: the exact sequence behind `skali auth login`
then `skali deploy` for a brand-new **bundle** on a single-node cluster (node A
holds all roles). The bundle is the canonical example from `00`: a SvelteKit app +
a Postgres **database** + an uploads **volume**. This is the 0.1.0 acceptance
scenario — the application spine **plus** the two data pillars.

## Preconditions

- `skalid start` is running on node A (master+edge+worker+builder) — which also
  brought up the managed `registry:2` container (the default `builtin` Registry)
  and prepared the quota-enforced storage path (`10`/`17`).
- A wildcard DNS record `*.apps.example.com` → node A's public IP.
- The developer has a project dir with a `Dockerfile`, no `.skali/` yet. The app
  reads `DATABASE_URL` and writes uploads to `/data/uploads`.

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
    name      = my-shop
    domain    = my-shop.apps.example.com
    node      = node-a            (only worker)
    replicas  = 1
    dockerfile= ./Dockerfile
    database  = main (postgres, shared)     # offered by the wizard
    volume    = uploads (10Gi) → /data/uploads
  → POST /api/v1/projects {name, registry=builtin}     (creates project + default
       `production` env + the `web` application)
  → POST /api/v1/projects/{id}/routes?env=production {host: my-shop.apps.example.com, port:3000}
  → master returns project id (prj_…)
  → CLI writes .skali/config.yaml (project.id + databases/volumes/binds, 11) + .gitignore
```

## Step 2 — apply topology: provision data resources & resolve bindings

```
  CLI applies the env's declared topology BEFORE building (11) — stateful data
  must exist before the app boots (03/17):
  → POST /api/v1/projects/{id}/volumes?env=production {name: uploads, size: 10Gi}
       master places it on node-a → Executor.EnsureVolume (quota set, headroom checked)
  → POST /api/v1/projects/{id}/databases?env=production {name: main, engine: postgres, allocation: shared}
       master ensures a shared Postgres POOL on node-a → Executor.EnsurePool
       → Executor.EnsureDatabase: CREATE DATABASE + isolated ROLE (no superuser)
       → derives DATABASE_URL (pool endpoint + db + role + generated password)
  → POST /api/v1/projects/{id}/bindings?env=production
       {app: web, database: main, inject: DATABASE_URL}
       {app: web, volume: uploads, mount: /data/uploads}
  (a binding referencing an undefined resource would ERROR here — deterministic, 11)
```

## Step 3 — upload context & build

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
     tag = <builtin-registry>/my-shop:<build_id>
     docker push → built-in registry (loopback on node-a)
     resolve pushed digest  (sha256:…)
  build logs stream: builder → master → client SSE (live in terminal)
  master records Build(image_digest, status=succeeded)
```

## Step 4 — release & reconcile (health-gated rollout)

```
  master creates Release(environment=production, image_digest, replicas=1, placement,
     env_snapshot = env vars + binding-injected DATABASE_URL) and marks is_current=1
  reconciler diffs desired vs actual for node-a (production env):
     (data resources from Step 2 already ensured — volume present, pool+DB ready)
     Executor.EnsureNetworks (per-app net + skali-proxy net + binding path to the pool, 02)
     Executor.PullImage(digest)            # already local → fast
     Executor.RunContainer(spec with Traefik labels:
         Host(`my-shop.apps.example.com`), service port 3000,
         healthcheck, skali.project/environment/release/instance labels,
         env DATABASE_URL=…, mount uploads → /data/uploads,
         networks (incl. binding-gated reach to pool), egress=internet firewalling)
     wait for container HEALTHY (health-gate)
  node-a Traefik (Docker provider) discovers the container via labels,
     begins routing the host to it
  reconciler records Instance(status=healthy)
```

## Step 5 — serve

```
  user → https://my-shop.apps.example.com
       → edge Traefik on node-a (TLS via ACME HTTP-01, terminates)
       → (edge==worker, no hop) routes via Docker labels → container :3000
       → app reads/writes Postgres (DATABASE_URL) + uploads (/data/uploads)
       → response
```

## Step 6 — finish

```
  master marks deploy succeeded
  CLI SSE shows rollout complete, prints:
     ✓ my-shop is live at https://my-shop.apps.example.com
       release rel_…  on node-a
  .skali/config.yaml already holds project.id → directory is linked
```

## Subsequent deploys

`skali deploy` now finds `.skali/config.yaml`, skips the wizard, applies topology
(data resources are **ensured, not recreated** — the Postgres DB and the uploads
volume **persist** across deploys, `17`), uploads context, builds a new image
digest, creates a **new Release**, and the reconciler does a
**start-new-then-stop-old** health-gated swap (`03`) — zero-downtime. The new
container comes up already wired to the *same* database and volume. A bad build
leaves the previous Release serving.

## Multi-node variation (when node B is added later)

Only steps 4–5 change for the **application**:

- Release placement targets node-b → reconciler calls **`RemoteExecutor`**
  (gRPC, mTLS) to pull+run on node-b.
- The master's edge HTTP-provider config now includes a `host → node-b` entry
  (remote-app), so edge Traefik on node-a forwards over the private network to
  node-b's proxy, which routes to the container. One extra hop; everything else
  identical.
- **Data resources stay pinned.** The pool/volume on node-a don't move; node-b's
  app reaches node-a's pool over the private net via the binding-gated path (`02`).
  Stateful relocation is `[future]` (`17`).

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
- **Staging gets its own data.** `staging`'s `main` database and `uploads` volume
  are **separate instances** from production's (realized per environment, `05`/`17`)
  — typically on a *shared* pool (cheaper) while prod runs `dedicated_host`. The
  app's `DATABASE_URL` differs per env automatically.
- **[soon]** after verifying, `skali promote staging production` releases
  **staging's exact digest** into production with production's own
  vars/routes/resources — **no rebuild** — advancing production by all the commits
  validated on staging.

This is the whole system exercised end to end. If steps 0–6 work on one node,
0.1.0 is met.
