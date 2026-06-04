# 11 — Project Config (`.skali/`)

Every deployed project carries a `.skali/` directory. It links the local
directory to a server-side Project and describes how to build/run it. It is
consumed by **both** the CLI and (later) git push-to-deploy, so they stay in
sync.

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
  name: my-website
  slug: my-website
  # build_per_env: true   # set if the build bakes env-specific config (NEXT_PUBLIC_*, …) — see 04

build:
  dockerfile: ./Dockerfile
  context: .
  builder: auto           # auto (builder node if any, else local) | local | <node-name>
  # args: { KEY: value }            # non-secret build args
  # [soon] secrets: [NPM_TOKEN]     # names resolved from server env at build

service:
  port: 3000              # container port the app listens on (→ Traefik service)
  replicas: 1
  # health:
  #   path: /healthz
  #   interval: 10s
  egress: internet        # internet | isolated
routes:
  - host: my-website.apps.example.com
env_file: .env            # gitignored; `skali env vars push` syncs it to production
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

environments:
  production:
    routes: [{ host: example.com }]
    service:
      port: 3000
      replicas: 3
      resources: { memory: 2Gi, cpu: 1.0, upload_limit: 100Mi }
    placement: { node: dedicated-box }   # run prod on a dedicated host
    env_file: .env.production
  staging:
    routes: [{ host: staging.example.com }]
    service: { port: 3000, replicas: 1 }
    env_file: .env.staging
    # access:                       # [soon] keep staging private (Traefik middleware, 02)
    #   basicauth: [ "client:$2y$..." ]   # user:bcrypt-hash; gate the URL behind a login
```

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
- **`config.yaml` declaratively owns environment topology; `apply`/`deploy`
  reconciles it.** The committed config is the **source of truth** for *which
  environments exist* and their non-secret shape (routes, resources, placement,
  replicas, env-file mapping). `skali env create` just edits this file; `skali
  apply` (or `skali deploy` for the target env) **reconciles the server to match
  it** — the same desired-state→converge model the runtime uses for releases
  (`03`). This is what lets you commit the config and have skalid bring the cluster
  to it (the basis for `[soon]` push-to-deploy auto-apply).
  - **Additive + drift-correct by default.** Apply creates missing environments,
    adds declared routes, and corrects declared fields that drifted back to the
    file. Resources **not** in the file (e.g. an env/route created ad-hoc in the
    web UI/API) are treated as *unmanaged* and left alone.
  - **Destructive changes are gated.** Removing an environment/route from the file
    does **not** delete it on the server unless you pass `--prune` (or confirm at
    the prompt) — so a bad or partial config push can't silently tear down
    production. CI opts into prune explicitly.
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
