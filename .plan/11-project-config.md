# 11 — Project Config (`.skali/`)

A `.skali/` directory links a local directory to a server-side **Project**
(a bundle — `17`) and declares its resources: the **application** (build/run),
plus the **databases**, **volumes**, and **bindings** that make it a whole system.
It is consumed by **both** the CLI and (later) git push-to-deploy, so they stay in
sync.

Two principles govern how the file relates to the server, both detailed below:

- **The config is a *requirements list*, not a full desired-state.** It is
  authoritative only for the fields it explicitly declares; anything it doesn't
  mention is *unmanaged* and owned out-of-band (web UI, CLI, server defaults).
- **A bundle can be *sharded* across repos.** One project may be declared by many
  `.skali/` dirs (polyrepo); shared resources resolve **by name** and conflicts
  are a deterministic error. So a monorepo and a four-repo system use the *same*
  model — only the file placement differs.

## Layout

```
.skali/
├── config.yaml      # COMMITTED — project identity + per-environment definition
└── local.yaml       # GITIGNORED — per-developer/local state (optional)

.env, .env.staging…  # GITIGNORED — env var VALUES, mapped to environments by config
```

The `config.yaml` carries the non-secret per-environment *differentiation*
(routes, resources, placement, and **which `.env` file** feeds each environment).
The `.env` files themselves hold the **values** and stay gitignored — the *mapping*
is committed, the *secrets* are not. skali auto-adds the mapped env files to
`.gitignore`.

`skali init` (or the first `skali deploy`) runs the setup wizard, creates the
server-side Project, and scaffolds this directory — the *same* shared flow either
way (see `09`). A recommended `.gitignore` entry (`/.skali/local.yaml`) is written
automatically.

## `config.yaml` (committed)

The shared, version-controlled config. **No secret values here** — only the
mapping to gitignored `.env` files.

**Single-environment shape (the simple default).** Top-level `service` / `routes`
/ `env_file` describe the implicit default environment (`production`). This is the
0.1.0 shape — most projects never need more:

```yaml
version: 1
project:
  id: prj_01HZX…          # server-assigned; written back on first deploy
  name: my-shop
  slug: my-shop

# the implicit single application (build + run). `build_per_env`, args, secrets
# belong to this application resource (05/04); top-level shorthand keeps it terse.
build:
  dockerfile: ./Dockerfile
  context: .
  builder: auto           # auto (builder node if any, else local) | local | <node-name>
service:
  port: 3000              # container port the app listens on (→ Traefik service)
  replicas: 1
  egress: internet        # internet | isolated
routes:
  - host: my-shop.apps.example.com
env_file: .env            # gitignored; `skali env vars push` syncs it to production

# managed data resources — declared as REQUIREMENTS; allocation is a policy (17)
databases:
  main:                   # → provisioned Postgres + injected DATABASE_URL
    engine: postgres
    version: "16"
    allocation: shared    # shared (pool) | dedicated | dedicated_host
volumes:
  uploads:                # → quota-enforced disk; the size_limit is ENFORCED (17)
    size: 10Gi

binds:                    # wire resources into the application
  - database: main        # → injects DATABASE_URL
  - volume: uploads
    mount: /data/uploads  # → mounts the volume here
```

**Multi-environment shape (config-driven, 0.1.0).** When you need staging/
production to differ, an `environments:` map replaces the top-level
`service`/`routes`/`env_file` (if present, it is authoritative). You declare it in
the file (by hand or via `skali env create`) and `skali apply`/`deploy` reconciles
the server to it. `build:` stays project-level — the image is shared and (with the
`[soon]` `skali promote`) reused between environments without rebuilding (`04`):

```yaml
build:
  dockerfile: ./Dockerfile
  context: .

binds:                                    # bindings are bundle-wide unless overridden
  - database: main
  - volume: uploads
    mount: /data/uploads

environments:
  production:
    routes: [{ host: example.com }]
    service:
      port: 3000
      replicas: 3
      resources: { memory: 2Gi, cpu: 1.0, upload_limit: 100Mi }
    placement: { node: dedicated-box }   # run prod on a dedicated host
    env_file: .env.production
    databases:
      main: { engine: postgres, version: "16", allocation: dedicated_host }  # prod gets its own box
    volumes:
      uploads: { size: 50Gi }
  staging:
    routes: [{ host: staging.example.com }]
    service: { port: 3000, replicas: 1 }
    env_file: .env.staging
    databases:
      main: { engine: postgres, version: "16", allocation: shared }          # staging shares a pool
    volumes:
      uploads: { size: 5Gi }
    # access:                       # [soon] keep staging private (Traefik middleware, 02)
    #   basicauth: [ "client:$2y$..." ]   # user:bcrypt-hash; gate the URL behind a login
```

Per-environment realization is the point: prod's `main` is the *same logical
database* as staging's `main` **by name**, but each environment gets its own
isolated instance, and the **allocation differs** (prod on a dedicated host,
staging on a shared pool) without changing the declaration. Same for volume sizes.

Notes:

- **`project.id` is the link.** Written back by the server on first deploy so the
  directory is bound. Committing it means teammates and CI deploy to the same
  project.
- Env **values** never live here — they live in the server DB **keyed by
  environment** (`05`) and are set via `skali env vars` (file sync or manual) or
  the web UI. `config.yaml` only declares **which `.env` file maps to which
  environment**.
- **The default environment is `production`.** The single-env shorthand *is* the
  `production` environment; adding an `environments:` block just makes the
  differentiation explicit and adds siblings like `staging`.
- **Route shape.** A route is `{ host, path: '/', port: 443 }` where `host` is
  required and `path`/`port` default to match-all on HTTPS. `port` here is the
  **external** listen port (`example.com:443` vs `example.com:8443`); the container
  `service.port` is the forward target. `(host, port, path)` must be unique
  cluster-wide (`02`), so the master rejects a route another env/project already
  claims.
- **[soon] multi-service:** `service:` becomes `services:` (or a committed
  `compose.yaml` is referenced) when Compose support lands. The single-service
  shape is a subset, so the upgrade is non-breaking.

## The config is a *requirements list* (partial desired-state)

The file declares **what the bundle requires**, not a complete snapshot of server
state. It is authoritative **only for the fields it explicitly mentions**:

- **Declared fields are reconciled** to the file. If `config.yaml` says
  `volumes.uploads.size: 10Gi`, apply makes the server match it (drift-correct).
- **Un-declared things are *unmanaged*** and left alone. Bump a volume's size or a
  replica count in the web UI, and — as long as the config doesn't pin that field
  — it **persists**. The config never silently reverts an out-of-band change to a
  field it doesn't claim. Declaring the field in the config later is what brings it
  back under file control.

This is what makes the file safe to be partial: a thin shard that declares only an
application + a `binds:` line doesn't have to restate (or risk clobbering) the
shared database's full spec. It is the same additive / drift-correct / gated-prune
`apply` model the server uses for everything (`16` decision #8), framed as the
mental model to design against. (We can fine-tune the managed/unmanaged boundary
per-field if a real case demands it.)

## Sharded config — one bundle, many repos

A Project can be **one app in one repo** (monorepo or single service) or **many
services across many repos** (a website, an admin panel, a Go API, a Rust worker —
each its own repo). skali supports both with **one model**: each repo has its own
`.skali/` shard declaring **only its own application** + the resources it needs;
they all carry the same `project.id` and resolve shared resources **by name**.
Monorepo vs polyrepo is then purely *where the shards live*, not a different
mechanism.

**Linking.** The first repo creates the project (`skali init`); the others attach
to it with `skali link --project <slug>` (writes the shared `project.id` into that
shard) instead of creating a new project.

**Resolution by name (the convergence rules).** A resource is identified by
`(project, environment, type, name)` — `database: main` is the *same* database
everywhere in the project. A shard declares a resource at one of two strengths:

- **Reference** — minimal: `binds: [database: main]`. "I use it."
- **Definition** — the full spec: `databases: { main: { engine: postgres,
  version: "16", allocation: shared } }`.

Across **all** shards of a project, the rules are:

1. **At most one *definition* per name.** Any number of references + one definition
   is fine. Two definitions that **disagree** → a **deterministic error** that
   names both shards and the diverging field (e.g. "`main` is postgres 16 in
   `api`, 15 in `web`"). Identical duplicate definitions are allowed but flagged as
   redundant (prefer a bare reference in consumers).
2. **A reference to a resource that *no shard defines* → error.** Provisioning is
   never implicit; you must define the spec somewhere (or run `skali db create`).
   This keeps `apply` deterministic. `[soon]` `skali config validate` pre-flights a
   config (and, where it can, the cross-shard picture) so you catch a conflict or a
   dangling reference **before** you push.
3. **Provision once, idempotently.** The first apply with a complete picture
   creates the one resource; later shards' applies only add **their binding**.

**Authority-scoped prune.** A resource's owner is the shard that *defines* it (or
`server` if created via CLI/UI). Only the owner's `apply --prune` (or an explicit
`skali db rm`) can remove it; dropping a *reference* in a consumer removes only
**that app's binding**. So the website repo can never prune the database the other
three depend on. Combined with the stateful no-auto-prune rule (`17`), a vanished
definition loses the *declarative spec*, never the *data*.

> **Why not sync the full config across repos?** It re-imposes monorepo coupling
> (every repo must know the whole system; a DB change touches four repos) without
> monorepo benefits, plus a drift/merge surface. "Reference, don't duplicate" keeps
> shards independent. Centralizing all *definitions* in one infra shard is still
> possible — it's just one convention on top of name-resolution, not a requirement.

## `local.yaml` (gitignored, optional)

Per-developer overrides that shouldn't be shared, e.g. pinning a non-default
context for this repo:

```yaml
context: work            # use the 'work' skali context for this project
```

## Relationship to clients & automations

- **CLI:** reads `config.yaml` to know which project/build/routes; if absent,
  runs the wizard and creates it.
- **[future] git push-to-deploy:** the same `config.yaml` drives server-side
  builds on push — the webhook handler reads it from the pushed tree, so CLI and
  git deploys are configured identically. This is the reason the config is a
  committed file in the repo rather than purely server-side state.
- **`config.yaml` is the requirements list for the bundle's topology;
  `apply`/`deploy` reconciles it.** The committed config is authoritative for the
  **declared** shape of *which environments exist* and their non-secret fields
  (routes, resources, placement, replicas, env-file mapping, **databases, volumes,
  bindings**). `skali env create` / `skali db` / hand-edits just change the file;
  `skali apply` (or `skali deploy` for the target env) **reconciles the server to
  match the declared fields** — the same desired-state→converge model the runtime
  uses for releases (`03`). This is what lets you commit the config and have skalid
  bring the cluster to it (the basis for `[soon]` push-to-deploy auto-apply).
  - **Additive + drift-correct by default.** Apply creates missing environments and
    resources, adds declared routes/bindings, and corrects **declared** fields that
    drifted. Fields/resources **not** in the file (an env/route created ad-hoc in
    the web UI, a storage limit nudged in the console) are *unmanaged* and left
    alone — the requirements-list rule above.
  - **Destructive changes are gated, and stateful data doubly so.** Removing an
    environment/route from the file does **not** delete it unless you pass
    `--prune` (or confirm) — so a bad or partial push can't silently tear down
    production. **Databases and volumes are never auto-pruned even with `--prune`
    unless explicitly confirmed**, and only by their owning shard (`17`,
    authority-scoped prune above) — a dropped reference removes only a binding,
    never the data. CI opts into prune explicitly.
  - **Build vs deploy still distinct.** `skali deploy` applies the target env's
    topology *and* builds+ships new source; `skali apply` reconciles topology only
    (no build, no release). Neither rebuilds other environments.
- **Secret values are never in the file and never pruned by apply.** Env var
  *values* live server-side, keyed by environment, and sync independently via
  `skali env vars push` from gitignored `.env` files — topology and secrets have
  separate lifecycles.
- **Env from mapped `.env` files (the primary workflow).** Each environment maps
  to a gitignored `.env` file in `config.yaml` (`env_file`). `skali env vars push
  [--env <name>]` reads that file and syncs the values to the server for that
  environment — **adding new keys freely and warning + confirming before
  overwriting any existing value**. So you keep `.env.production` / `.env.staging`
  locally and push them up per environment, instead of setting each var by hand.
  `skali deploy --env-file` does the same sync inline for the target environment.
  Values still never live in committed `config.yaml`; only the file *mapping*
  does.
