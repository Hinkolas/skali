# 09 — `skali` CLI

The developer-facing client. **cobra** for the command tree, **charmbracelet/huh**
for interactive configuration (the scaffolder-style prompts on first deploy). It
talks only to a master over REST (`06`).

## Client config & contexts

Stored under XDG: `~/.config/skali/config.yaml`.

```yaml
current_context: home
contexts:
  home:
    master: https://skali.home.example.com
    token:  <opaque token>           # from `skali auth login`
  work:
    master: https://skali.work.example.com
    token:  <opaque token>
```

- **Multiple contexts** (kubectl-style) from day one, since the user runs more
  than one cluster (office + Hetzner). `skali context use <name>`.
- Token stored with `0600` perms. `[future]` OS keychain integration.

## Command tree

All project-scoped commands target an **environment**; `--env <name>` selects it
and defaults to the project's default environment (`production`).

```
skali
├── auth
│   ├── login      # POST /auth/login → store token in current context
│   ├── logout     # revoke token, clear from config
│   ├── whoami     # GET /auth/whoami
│   └── status     # show current context + connectivity
├── context
│   ├── list
│   ├── use <name>
│   └── add <name> --master <url>
├── init            # run the setup wizard: create the project (+ default env) + write .skali/ (no deploy)
├── deploy [dir]    # reconcile target env's topology from config + build new source + ship it; --env
├── apply           # reconcile environment topology (config.yaml → server); no build/release; --env, --prune
├── promote <from> <to>   # [soon] reuse the source env's current image → release to target env (no rebuild)
├── redeploy        # re-release current image with latest env/config, roll out; --env
├── restart         # bounce the current Release's instances (no new Release); --env
├── projects
│   ├── list
│   ├── show <id|name>
│   └── delete <id|name>
├── env             # ENVIRONMENTS (production, staging, …) — the deployment target
│   ├── list                  # environments declared in config + on server (flags drift)
│   ├── create <name>         # scaffold/adjust the env in .skali/config.yaml (LOCAL; apply with `skali apply`/`deploy`)
│   ├── show <name>
│   ├── use <name>            # set the default env for this dir (.skali/local.yaml)
│   └── vars                  # VARIABLES for an environment (server-side, keyed by env)
│       ├── push [--env <name>]   # PRIMARY: sync the env's mapped .env file → server (warn+confirm on overwrite)
│       ├── pull [--env <name>]   # [soon] write the server's vars back into the .env file
│       ├── list [--env <name>]   # list keys (values masked)
│       ├── set KEY=VALUE [...]    # manual set (secondary path); stores only — apply with `skali redeploy`
│       └── rm KEY
├── logs [project]  # SSE tail of runtime logs; --env
├── builds          # async build/deploy jobs (deploy returns a job id; track here)
│   ├── ls                    # active + recent jobs + status (queued/building/pushing/rolling-out/done/failed)
│   ├── status <id>
│   ├── logs <id>             # attach the SSE stream (build + rollout) for a job
│   └── cancel <id>           # [soon] cancel a queued/running job
├── releases        # per-environment history
│   ├── list                  # --env
│   └── rollback <release-id>
└── domains         # routes for an environment; --env
    ├── list
    ├── add <host>
    └── rm <host>
```

## `skali deploy` behavior

1. Resolve target dir (default cwd), current context (must be logged in), and
   **target environment** (`--env`, else the dir's sticky env from `local.yaml`,
   else the project default `production`).
2. **If not initialized** (no `.skali/config.yaml` with a `project.id`), run the
   **shared "ensure initialized" primitive** — the *same* flow `skali init` uses:
   - **Interactive (TTY):** run the huh wizard (project name, domain, target node,
     replicas, build settings) → create the project via REST → write
     `.skali/config.yaml` with the returned id, then continue to deploy. This is
     the frictionless "just run `skali deploy`" path.
   - **Non-interactive (no TTY / `--no-input` / CI):** the wizard can't prompt, so
     deploy **errors and tells the user to run `skali init`** (or pass the
     required flags, e.g. `--name`, `--host`, `--node`).
3. **If `.skali/` exists:** read project id, build config, and the target env's
   declared topology. Deploy **reconciles that env's topology** to the server first
   (the additive/drift-correct `apply` step; destructive only with `--prune`), then
   builds — so routes/resources declared in `config.yaml` take effect on deploy
   (see `11`).
4. **Optional env sync.** With `--env-file` (or, by default for the target env,
   the file mapped in `config.yaml`'s `env_file`), push those vars into the target
   **environment** — adding new keys freely, but **warning and confirming before
   overwriting any existing value**. Same operation as `skali env vars push`, run
   inline.
5. **Build, by location** (`build.builder` / `--builder`, see `04`):
   - **node** (`auto` with a builder present, or a named node): tar the context
     (respect `.dockerignore`) → `POST /projects/{id}/deploys?env=<name>`
     (multipart upload); the builder node builds + pushes.
   - **local** (`--builder local`, or `auto` with no builder): build here via local
     Docker, push the digest to the registry, then `POST
     /projects/{id}/deploys?env=<name>` with `{ image_digest }` (no context upload).
6. **Async by default.** As soon as the server has everything it needs, `skali
   deploy` prints the **job id** and **returns (exit 0)**; build + rollout proceed
   server-side. "Has everything" means: for a **node** build, the context upload
   finished; for a **local** build, the image was built+pushed *here* (that part is
   necessarily foreground — it's your machine) and the release-from-digest was
   submitted (the rollout is then async).
7. **Follow only if asked.** `--follow`/`-f` attaches the SSE stream and renders
   build + rollout live (the old synchronous feel), exiting with the job's result.
   Otherwise track it via `skali builds ls` / `skali builds logs <id>` /
   `skali builds status <id>`. On a followed success, print the live URL(s); if the
   project was new, the id is already persisted to `.skali/` (step 2).

Flags: `--env`, `--builder` (`auto`|`local`|`<node>`), `--follow`/`-f` (attach and
stream instead of returning), `--node` (placement), `--no-cache` (build),
`--dockerfile`, `--context-dir`, `--env-file`.

## `skali redeploy` / `skali restart` (applying changes)

`skali env vars push`/`set` (and other server-side config edits) **store only —
they do not roll out.** Staged changes are applied to the target environment
either by the next `skali deploy` (which also rebuilds) or explicitly by:

- **`skali redeploy [--env]`** — create a new Release from the environment's
  **current image digest** + its latest env/config snapshot and roll it out
  (health-gated swap, `03`). Applies env/config changes **without rebuilding**.
- **`skali restart [--env]`** — bounce the environment's current Release's running
  instances (stop/start) without creating a new Release; for "just restart it"
  cases. A plain restart re-runs the *same* Release, so it does **not** pick up env
  changes — use `redeploy` for that.

## Environments & variables

`skali env` manages **environments** — named running configurations of one project
(`production`, `staging`, …). Every project has a default `production` environment;
you only reach for the rest of this when you want a second one. Environment
topology is **declarative**: you describe it in `config.yaml` and reconcile the
server to the file.

- **Create = edit the file.** `skali env create staging` **scaffolds/adjusts the
  `environments.staging` block** in `config.yaml` (routes, resources, placement,
  mapped `.env`) — a **local** operation, no server call. Hand-editing the YAML is
  equivalent. `skali env use staging` makes it this directory's sticky default
  (`.skali/local.yaml`); otherwise pass `--env`.
- **Apply = reconcile the file to the server.** `skali apply` brings the server's
  environment topology in line with `config.yaml`: it **creates missing envs, adds
  declared routes, and drift-corrects declared fields**, leaving unmanaged
  (file-absent) resources alone. Removing something from the file deletes it on the
  server **only with `--prune`** (or an interactive confirm), so CI can opt into
  destructive sync while a stray local edit can't nuke prod. `skali deploy` applies
  the **target env's** slice automatically before building. (Model + rationale:
  `11`.)
- **Variables are file-first and separate from topology.** Each env maps to a
  gitignored `.env` file. The loop is **edit the file, then `skali env vars push
  --env <name>`** — not setting vars one at a time. Push **adds new keys freely and
  warns + confirms before overwriting** existing values (`11`); `apply` never
  touches var *values*. `skali env vars set KEY=VALUE` exists for one-offs. All of
  these only *store* (apply to running instances with `redeploy`).
- **Promote, don't rebuild.** `skali promote staging production` (`[soon]`) takes
  staging's **current image digest**, releases it to production with production's
  own vars/routes/resources, and rolls out — **no second build** (`04`). This is
  the "verify on staging, then ship the exact thing to prod" path; production
  jumps to staging's validated image in one release.

## `init` vs `deploy` (one shared wizard)

The setup wizard is **one implementation**, not duplicated. Both commands go
through the same "ensure initialized" primitive:

- **`skali init`** runs the wizard, creates the server-side Project, writes
  `.skali/config.yaml` (with `project.id`) + the `.gitignore` entry, and
  **stops**. Use it to set a project up without deploying yet (a Project with zero
  Releases is a valid state).
- **`skali deploy`** calls the same primitive first: if the directory is already
  linked (`project.id` present) it proceeds straight to build+ship; if not, it
  runs the wizard inline (interactive) or instructs the user to `skali init`
  (non-interactive). So `deploy` **reuses** init's logic rather than copying it.

"Initialized" has one definition everywhere: `.skali/config.yaml` exists with a
real `project.id` that resolves on the server. (If the id is present but the
server returns 404 — project deleted — deploy reports it and offers to re-init
rather than silently recreating.)

## Interactive wizard (huh)

The first-deploy questionnaire mirrors a framework scaffolder. It configures the
project's **default `production` environment** (additional environments come later
via the `environments:` block / `skali env create`):

- Project name (defaults to dir name) → validated/sluggified.
- Domain/host (offer `<slug>.<cluster-default-domain>` if the cluster has a
  default wildcard configured; allow custom domain).
- Target node (list from `GET /nodes`; default the only/first worker).
- Replicas (default 1).
- Build: Dockerfile path (autodetect `./Dockerfile`), context dir.
- Confirm → create.

Non-interactive escape hatch: every prompt has a corresponding flag so
`skali deploy` works in CI/scripts without a TTY (huh is skipped when flags fully
specify the config or when stdin is not a TTY).

## UX details

- Async deploy exits 0 once the job is **accepted** and prints the job id +
  `skali builds` hint. With `--follow` (or a client-side local-build failure) it
  exits with the **job's** result and surfaces the server error envelope; otherwise
  failures show up in `skali builds ls`/`status` (and `[soon]` push notifications).
- Followed runs show clear streamed build output + a final summary block (URL,
  release id, node).
- `skali` is a thin REST client and never talks to gRPC. **One exception:**
  `--builder local` (`04`) drives **local Docker** to build + push the image — the
  only time the CLI touches Docker/the registry. Everything else is REST.
