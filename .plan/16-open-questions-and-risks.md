# 16 — Open Questions & Risks

Decisions we have **not** made, and design tensions to revisit. Several of these
were surfaced while writing the plan down (the user asked me to reconsider the
design as I documented it). Each notes a recommendation so we have a default.

## Surfaced while writing (new since the discussion)

### 1. Reconciliation vs imperative control *(resolved in plan, confirm)*
The discussion implied imperative "master tells node to run X." That breaks on
reboots/crashes/restarts. **Plan adopts a declarative reconciler** (`03`):
desired state in SQLite, workers report actual via Docker events, master
converges. **Recommendation:** keep this — it's the right backbone and gives
container crash-recovery for free. **Decided:** confirmed as the core runtime
model. Node startup resync uses the *same* code path as steady-state reconcile
("node starts → gets desired state → reproduces it"); robustness principles
(idempotency, self-describing containers, level-triggered convergence, single
writer) are written into `03`.

### 2. Zero-downtime rollout *(was undefined)*
We never specified how a redeploy swaps versions. **Plan: start-new-then-stop-old,
health-gated** (`03`), with automatic "keep old on failed deploy." **Open:** do
we need configurable strategies (blue/green, canary) in 0.2, and what's the
default health signal when an app declares none? **Decided:** when a Release
declares **no** health check, the new Instance is treated as healthy **as soon as
it starts** and the old Instance is stopped immediately — no grace period.
Documented tradeoff (`03`): a new image that starts then crashes can cause a brief
outage; declaring a health check restores the safety net. Configurable
blue/green/canary strategies remain `[soon]`.

### 3. Secrets at rest *(was plaintext)*
Plaintext env/creds/CA-key in SQLite is a real risk. **Decided: deferred out of
0.1.0.** 0.1.0 stores env/creds/CA-key as **plaintext** in SQLite (protect via
host + file permissions). A dedicated **[soon]** security pass adds AEAD
encryption keyed by an operator-provided master key (`master_key_file`, 0600) not
stored in the DB; the columns are designed to hold plaintext now / ciphertext
later, so it's an additive migration. KMS/Vault stays `[future]`. (`05`, `08`,
`15`.)

### 4. Registry: built-in managed vs external *(decision flipped)*
Single-node doesn't strictly need a registry (the image is already local) — but
images are the **rollback substrate** (a Release pins a digest; rollback
re-releases it), so the registry is **lifecycle state skali must own**, not
transient transport. **Decided (revised from "connect external"):** 0.1.0 ships a
**built-in managed `registry:2` container**, supervised by skalid as a **core
dependency alongside Traefik**, as the default artifact store. Rationale: (a) skali
owns retention/GC instead of leaving cruft in someone else's registry; (b) the
registry sits inside the cluster trust boundary, so builders/workers authenticate
with their **mTLS node identity** — and note the **CLI never pushes** (builds are
server-side), so there was never a CLI-registry credential to unify. Connecting an
**external** registry stays supported via the same `Registry` abstraction (e.g. a
dedicated *release* registry for promotion). The **always-through-the-registry**
model is unchanged — build pushes, deploy pulls, fast loopback no-op when
co-located. (`04`, `10`, `15`.)

### Environments, variables & promotion *(new — resolved)*
Surfaced when the user asked for a staging→production workflow. **Decided:**
- **Environment is a first-class entity** between Project and Release (`05`). A
  Project is the *build* unit; an **Environment** (`production`, `staging`, …) is a
  *running configuration* owning its routes, env vars, replicas, placement,
  resource limits, and release history. Every project gets an implicit default
  `production` env so the single-env path is unchanged (progressive disclosure).
  **Config-driven multi-env management is in 0.1.0** (per the user): `skali env
  create` edits `config.yaml`; `skali apply`/`deploy` reconciles the server to it
  (decision #8). Only **image promotion** (`skali promote`, reusing a digest across
  envs) is **[soon]**.
- **Topology committed, values not.** `config.yaml` carries each env's non-secret
  differentiation **and a mapping to a gitignored `.env` file**; env **values** live
  server-side keyed by environment. The primary var workflow is **file-first**:
  edit `.env.<env>`, `skali env vars push` (warn+confirm before overwriting).
- **Naming.** `skali env` is the **environment**; variables nest under it (`skali
  env vars …`). Resolves the env/var noun collision.
- **Promote, don't rebuild.** Because env is injected at **runtime**, the same
  digest serves all envs; `skali promote staging production` reuses staging's
  digest with production's config. **Caveat:** build-baked config (`NEXT_PUBLIC_*`)
  forces `build_per_env` (separate image per env). (`04`, `05`, `06`, `09`, `11`.)

### 5. Certificate strategy: wildcard DNS ≠ wildcard cert *(easy to conflate)*
Pointing `*.apps.example.com` gives wildcard **routing**, but per-host **certs**
via HTTP-01 need no DNS creds, while a wildcard **cert** needs DNS-01 + provider
API (`02`). **Plan: HTTP-01 per-host for 0.1.0**, DNS-01 wildcard `[soon]`.
**Open:** do you want DNS-01 sooner (e.g. you expect many short-lived subdomains
where per-host issuance is annoying)? **Recommendation:** HTTP-01 first; add
Cloudflare DNS-01 early in 0.2 since you control your domains.

### Build location & project identity *(new — resolved)*
Three related flows the user raised (quick deploy of a legacy/un-setup repo):
- **Local build is a 0.1.0 core feature.** Build cost ≠ run cost: weak cloud VPSs
  run apps fine but build slowly. `build.builder` (`auto`|`local`|`<node>`) selects
  where a build runs; `auto` uses a `builder`-role node if present, **else local**;
  `local` builds on the dev machine (`skali` CLI + local Docker) and pushes to the
  registry. Reuses the release-from-digest path — everything still goes through the
  registry. **Wrinkle:** the registry must be client-reachable (authenticated HTTPS
  endpoint) for local push, and the master mints a short-lived scoped push token —
  the one exception to "the CLI never touches the registry." (`04`, `01`, `06`, `09`.)
- **Repo auto-detection `[soon]`.** On `init`/`deploy`, read `git remote origin` and
  **offer** to link a connected GitHub repo (pre-fill the wizard). Detect-and-suggest
  only — never silent-link, since the remote isn't always the deploy source.
- **Project identity: one `.skali` config ↔ one project.** A repo may hold many
  projects (monorepo subdirs), but a single directory binds to exactly one project;
  silently spawning multiple projects from one dir is disallowed (no clean identity
  anchor, breaks CI/push-to-deploy). "Quick and dirty" is just `skali deploy` with
  cheap defaults + local build; **"temporary" is a lifecycle property (TTL)**, not a
  new entity — applicable to a project (throwaway new code) or an environment
  (preview of a known app, `[future]`). Previews are unmanaged/auto-expiring, so the
  declarative `apply` model already tolerates them. (`15`, `11`.)

### Build queue & architecture awareness *(new — resolved)*
- **Build job queue, no broker.** Server-side builds run async through the `builds`
  table as a queue; the **master is the scheduler** (single writer), dispatching to
  builders over gRPC (push, not worker-pull — pull would break single-writer). No
  Redis/NATS/RabbitMQ — that would violate the self-contained / SQLite-source-of-
  truth ethos. Builds are **run-once jobs** (retry-bounded), kept distinct from the
  level-triggered instance reconciler. Policy: per-builder concurrency cap
  (`nodes.build_concurrency`), cache-sticky assignment, backpressure via `queued`,
  requeue on builder death. **0.1.0 = degenerate single-builder queue;**
  multi-builder distribution is additive `[soon]`. Local builds bypass the queue.
  (`04`, `05`, `03`.)
- **Architecture reporting locked in.** Every host reports its arch (`amd64`/
  `arm64`, from the Docker engine) — nodes via Heartbeat (`nodes.arch`, `07`), the
  CLI knows its own for local builds. **0.1.0: native matching only** — a build's
  `target_platform` needs a builder of that arch, and Releases place only on
  matching-arch nodes; a mismatch is a **clear error**, not an unrunnable deploy.
  **[future]: emulated cross-arch (QEMU/buildx)** — kept deliberately because a
  powerful arm64 builder emulating amd64 often beats a weak amd64 building natively
  — plus multi-arch manifests. (`04`, `05`, `03`.)

### Async deploys & source transport *(new — resolved)*
- **Deploy is async by default.** `skali deploy` returns a **job id** as soon as the
  server has what it needs, and proceeds server-side; track via `skali builds
  ls`/`status`/`logs`, or `--follow`/`-f` to attach the SSE stream (old synchronous
  feel). For a **local** build the build phase is necessarily foreground (it's the
  CLI's own machine); only the release/rollout is async. (`09`, `06`.)
- **Source transport — remote build does *not* require a connected repo.** Three
  paths: local (no transport); **remote + context-upload tar** (the no-repo case);
  remote + git-clone (`[soon]`, repo-connected, efficient/push-to-deploy). We
  **rejected** "force local build when no repo" because it breaks the core
  weak-client→powerful-builder use case (it would push builds back onto the slow
  machine). The async queue means an uploaded context is **persisted as a
  content-addressed blob** (master data dir, keyed by `cli-upload:<hash>`), fetched
  by the assigned builder, and **GC'd at terminal build state** — the registry image
  is the durable artifact. (`04`, `05`, `07`.)

### Unified notification system *(new — captured, roadmap 0.3.x)*
One event core + delivery engine; **transports plug in** (webhook, email, `[future]`
mobile push) — building it transport-by-transport would duplicate the hard part
(reliable delivery), so the engine is shared and only the *send* step differs.
- **Event taxonomy** across the lifecycle: `deploy.*` (queued/succeeded/failed),
  `build.failed`, `release`/`rollout`/`rollback`/`promote`, `instance.unhealthy`/
  `crashed`/`recovered`, `node.*`, `cert.*`. Headline ones mirror "push / workflow
  fail." Define the catalog once; it's cross-cutting.
- **Subscriptions** scoped cluster/project/environment, with an event filter, a
  **transport**, a target, and a secret. CRUD via REST/CLI/UI (`06`).
- **Delivery = outbox on SQLite** (no broker — same pattern as the build queue): an
  event row + per-subscription delivery rows are written **in the same transaction**
  as the state change, then a master-side dispatcher delivers them. **At-least-once**
  with retry/backoff, **auto-pause** after N consecutive failures, a delivery log,
  and a test ping. Receivers **dedupe on the delivery id**.
- **Master is the sole emitter** (single source of truth); workers report, they
  don't emit. Keeps ordering/auth simple.
- **Transports:**
  - **Webhook** — HMAC body signature (`X-Skali-Signature-256`) + timestamp;
    versioned JSON envelope `{ id, type, created_at, cluster, project?, environment?,
    data{} }`. The outgoing counterpart to incoming push-to-deploy.
  - **Email** — needs a **cluster SMTP / email-provider** setting (`/notifications/
    settings`, `06`).
  - **[future]** mobile push (APNs/FCM), native Slack/Discord.
Depends only on the event stream existing, so it could land earlier than 0.3.x.
Generalizes the `[soon]` CLI job-completion notifications (one transport among many).
The **mobile app** (`[future]`, `15`) is both a push *transport* and another REST
client (status + restart/stop controls) — no new control surface. (`15`, `06`.)

### App access protection (private staging) *(new — captured, roadmap 0.2.x)*
Use case: share a staging site with a customer for review without making it public.
**Decided:** an **access policy attached to a route/environment**, rendered as a
**Traefik middleware** (declarative, like routing labels) — *not* app code.
**Basic-auth first** (per-route/env `user:bcrypt-hash` → Traefik `basicAuth`); the
hashes are stored server-side, not plaintext. Escalate only if it's not enough:
`[future]` IP allowlist (`ipAllowList`), forward-auth + custom login / SSO
(`forwardAuth` → a skali auth service), signed time-boxed preview links. Applied at
whichever proxy tier terminates the request. (`02`, `06`, `11`.)

## Carried-over / still genuinely open

### 6. Master is a single point of failure
SQLite-on-master means no control-plane HA. Data plane survives a master outage
(running apps keep serving; Traefik caches config), but no changes can be made
and config goes stale. **Recommendation:** accept for 0.1.0; document it; plan
litestream backups now as the stepping stone, real HA `[future]`. *Decide how
much this bothers you for your own use.*

### 7. gRPC connection direction under NAT
Plan assumes **master dials worker** (fine for "mutually reachable private
network"). If a worker is behind NAT and can't accept inbound, we'd need the
worker to dial out and hold a reverse stream. **Recommendation:** keep
master-dials-worker for 0.1.0; design the gRPC layer so a reverse-dial transport
can be added without changing the `Executor`/service layers. *Do any of your
target topologies have un-routable workers?*

### 8. Config drift: committed `.skali/config.yaml` vs server-side edits *(revised → declarative)*
Who wins when the file and the server disagree? **Decided (revised from "deploy
never reconfigures"):** `config.yaml` is the **source of truth for environment
topology** (which envs exist + their routes/resources/placement/replicas). `skali
apply` — and `skali deploy` for its target env — **reconciles the server to the
file**, the same desired-state→converge model the runtime uses for releases. Kept
safe by:
- **Additive + drift-correct by default**; resources absent from the file are
  "unmanaged" and left alone (an ad-hoc web-UI route survives).
- **Destructive sync gated** behind `--prune`/confirm, so a partial or bad config
  push can't silently delete an environment or route.
- **Secrets out of band**: env var *values* live server-side keyed by environment
  and sync via `skali env vars push` from gitignored `.env` files; apply reconciles
  **topology only** (it can neither leak nor clobber secret values).

This supersedes the earlier "deploy only builds, never reconfigures" stance — the
declarative model is cleaner and is exactly what makes `[soon]` push-to-deploy
auto-apply coherent (push the config, skalid converges the cluster to it). (`09`,
`11`, `06`, `02`.)

### 9. Does an env change auto-redeploy?
Env is snapshotted into a Release. Changing env via `skali env vars push`/`set` —
does it create a new Release immediately or wait for the next deploy? **Decided:**
`skali env vars` **stores only**, no rollout. Changes apply on the next
`skali deploy`, or explicitly via **`skali redeploy`** (new Release from the
current image + latest env, health-gated rollout, no rebuild). **`skali restart`**
bounces the current Release's instances (same Release, so it does not pick up env
changes). (`09`, `06`.)

### 10. Build resource isolation / multi-tenant builds
BuildKit builds run on a `builder` node with access to the Docker daemon. Builds
are effectively privileged. For untrusted multi-tenant use this is a risk.
**Recommendation:** fine for single-operator 0.1.0; for multi-tenant `[future]`,
consider rootless BuildKit / isolated build sandboxes.

### 11. Per-environment deploy concurrency
Two deploys racing the reconciler. **Plan uses a per-environment lock** (`05`) —
deploys/promotes targeting the same Environment serialize; different environments
of one project deploy concurrently. **Open:** queue vs reject-second.
**Recommendation:** serialize (queue) per environment; reject only if a deploy is
already queued+running and `--no-wait`.

### 12. Log persistence
0.1.0 streams logs on demand from Docker (no storage). **Open:** do you want
persisted/searchable logs? **Recommendation:** on-demand for 0.1.0; pluggable log
shipping `[future]`. Note this means logs vanish with the container.

### 13. Naming: "master"
We use `master` for the control-plane role (matches your wording). Some projects
prefer `controller`/`server`/`primary` for tone/clarity. **Recommendation:**
cosmetic; decide before it's user-visible in CLI/docs. Low stakes.

## Smaller things to pin during implementation

- Route uniqueness key = `(host, listen_port, path_prefix)`, enforced cluster-wide
  via a unique index (`05`, `02`). `listen_port` defaults to 443; the container
  `service_port` is the forward target, **not** part of the key. Non-443
  entrypoints and raw TCP routing are a `[soon]` edge extension — no schema change.
- ID format (ULID vs UUIDv7) — recommend ULID (sortable, compact, URL-safe).
- Default cluster domain handling (does a cluster store a default wildcard so the
  wizard can offer `<slug>.<default>`?). Recommend: yes, a cluster setting.
- Checked-in generated code vs `make generate` — recommend checked-in for sqlc &
  proto to ease onboarding.
- Whether `skali` and `skalid` ship as one combined binary with subcommands or
  two — plan assumes **two** (clear separation, smaller client). Confirm.
