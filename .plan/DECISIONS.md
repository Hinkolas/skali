# Decisions Log

Resolved design decisions, append-only, newest first. This is the "what we chose
and why" record so `16-open-questions-and-risks.md` can hold **only what's still
open**. Each entry is terse; the cited chapters carry the detail.

---

## Control-plane database: skalid-managed Postgres `[soon]`; external `DATABASE_URL` is the default today

Two ways skalid gets its control-plane Postgres:

- **Self-managed `[soon]`:** once the container engine exists, `skalid` spins up
  and supervises its own Postgres container (the same ownership pattern as
  Traefik/registry) and connects to it — the operator manages no dev/prod DB
  dependency at all. The published port is configurable via a server flag
  (e.g. `--db-port 54321`).
- **External (the default now, and always available):** point `DATABASE_URL` at
  any existing Postgres — managed cloud DB, host-installed, or a shared dev
  container. This is the only implemented path until the container engine
  lands.

Local dev uses the machine-global shared dev containers (`~/Taskfile.yml`,
`task -g db:start`) — no per-project compose files. Project automation uses
**go-task, not make** (`Taskfile.yml`).

## Technical foundation = smbx port; `.plan` is authoritative for UX/DX only

**Decided:** the implementation mirrors the smbx project (`~/Github/hinkolas/smbx`)
— its tech stack, code structure, and auth system — refactored from
username+password to **email+password**. The `.plan/` chapters remain authoritative
for **UX/DX** (command trees, endpoint surface, product feel); where their
*technical* choices conflict with smbx, smbx wins and the chapters get revised
opportunistically as implementation proceeds.

- **Stack:** Go + chi, **Postgres + pgx/sqlc/goose** (control plane; supersedes
  SQLite in `05`), UUIDv7 ids (supersedes ULID pin in `16`), env-driven config via
  go-envconfig/godotenv (supersedes the `skalid.yaml` config file in `10` for now),
  slog + OTEL. Dev DB via `compose.yaml`; production later runs Postgres as a
  skalid-supervised container — same ownership pattern as Traefik (`10`).
- **Auth:** smbx system ported whole — opaque tokens (sha256-hashed, sliding 30d
  sessions), argon2id, login rate limiting, full TOTP 2FA + backup codes + login
  challenges, session management. Aligns with `08`'s opaque-token decision; the
  `05` users/tokens DDL is superseded by smbx's users/accounts/sessions schema.
- **Web UI = SvelteKit BFF (adapter-node), just another API client.** One
  token-based API for all clients (web BFF, CLI, native `[future]`). The BFF owns
  the browser cookie; the token never reaches browser JS. **Supersedes `13`'s
  adapter-static + `go:embed` + "no Node on servers"** — the BFF becomes a
  skalid-managed container `[soon]`, same supervision pattern as Traefik.
- **Bootstrap:** no public signup; users created via `skalid user create`
  (operator CLI, smbx pattern). Single-admin 0.1.0 unchanged.

## Platform scope: three resource pillars (app + database + storage)

skali is a **hosting platform** (the Sevalla idea), not just an app deployer (the
Coolify part). A **Project is a bundle** of **typed resources** — `application`,
`database`, `volume` — that **bind** to each other. **Decided:** adopt this as the
core model now and build the three pillars **horizontally** (breadth-first) into
0.1.0, rather than finishing the app deeply first. (`00`/`17`/`15`.)

- **Project = bundle; Application = a resource.** `Project` is the logical system;
  the buildable app is one resource within it (1 per project in 0.1.0, N `[soon]`).
  Builds hang off `applications`, not `projects`. (`05`/`01`.)
- **Allocation decoupled from declaration.** The config declares *"I need a
  database"*; shared-pool vs dedicated vs dedicated-host is a placement **policy**,
  not a different product concept. (`17`.)
- **Bindings generate the ergonomics.** app⟷database → injected `DATABASE_URL`;
  app⟷volume → a mount. N apps may share one resource. (`17`/`05`.)

## Storage: disk/volumes only; S3 deferred

**Decided:** the 0.1.0 storage pillar is **disk/volumes only**. Object storage
(S3/R2/MinIO) is `[future]` and arrives as a *connect-and-surface-in-UI*
convenience (like an external registry), **not** a provisioned/mounted resource
type. (`16`/`17`/`15`.)

## Disk-safety floor (a 0.1.0 invariant, not deferred)

The "runaway volume filled the host disk and killed every app" failure gets a hard
answer: **every volume has an enforced `size_limit`**, volumes live on a partition
isolated from host/control data, and skalid **reserves headroom** and refuses to
provision past it. A runaway fills its own quota, blast radius of one. (`17`/`10`.)

## Volume quota enforcement = XFS project quotas on a skali-managed filesystem

**Decided** (was `16 §A`): the per-volume hard cap is enforced with **XFS project
quotas**, on a filesystem **skali owns**.

- **skali owns one XFS filesystem** for `storage.volumes_dir`: a native XFS
  partition if the operator points `volumes_dir` at one, **else a managed image/LV
  that skalid creates** (`mkfs.xfs`, mounted once with `prjquota`). So there is
  **no operator disk-setup prerequisite** — "install skalid, get quota'd volumes."
- **Each volume = its own XFS project ID + a hard block limit** (`size_limit`).
  Writes past it get `ENOSPC` immediately (kernel-level, no polling race).
  Resizing a volume = change the project quota number (online, trivial); growing
  the whole store = grow the backing + `xfs_growfs`.
- **One managed FS, not one loop device per volume** — so good performance for the
  fsync-heavy database pools (whose data dirs are volumes too), and pool data may
  optionally be placed on a **native XFS partition** for max throughput.
- **Host floor** holds because the volumes FS is bounded and separate from root +
  control data, with reserved headroom.
- **Monitoring is always-on** (per-volume usage metrics + a breach alarm) as a
  backstop, regardless of the hard cap.
- **`quota_backend` stays pluggable** (`10`): native-XFS-partition is the same
  backend with a different backing; **ZFS datasets / btrfs qgroups are `[future]`
  backends** for operators who already run them.

**Rejected:** loopback-per-volume (per-volume loop devices + double-layering hurt
DB performance, clunky resize) as the *default* — kept conceptually as the
zero-XFS fallback only if needed; **monitor-and-act alone** (soft — a fast writer
overshoots between polls, fails the "never happen" bar). (`17`/`10`/`16`.)

## Stateful carve-out to the reconciler

"Workers are stateless" holds for application instances only. **Databases, pools,
and volumes are stateful:** pinned to a node, provisioned once, **ensure-only**
(never destroy-to-recreate on drift), never auto-rescheduled, never auto-pruned;
teardown is always explicit/gated. This is the deliberate exception that makes
managed data safe. (`03`/`01`/`17`.)

## Managed databases (Postgres first) + backups

**Decided:** skalid supervises **Postgres pools** as owned infrastructure (like
Traefik/registry); a shared pool hosts many isolated logical DBs (own role, no
superuser); reachability is **binding-gated** east-west TCP (not Traefik).
**Backups are first-class** (the named Coolify pain): scheduled `pg_dump` to a
backup-target volume, with restore. MySQL/Redis and PITR are `[later]`. (`17`/`10`/
`08`.)

## Config is a requirements list (partial desired-state)

**Decided:** `config.yaml` is authoritative **only for the fields it declares**;
anything it doesn't mention is *unmanaged* and owned out-of-band (web UI / CLI /
server defaults) and persists. This is the clean framing of the additive /
drift-correct / gated-prune `apply` model. (`11`/ supersedes the topology framing
of decision #8 below.)

## Sharded config — one bundle, many repos (polyrepo)

**Decided:** a Project may be declared by **many `.skali/` shards** (polyrepo);
monorepo vs polyrepo is just *where the shards live*. Shared resources resolve **by
name**:

- **Reference vs definition.** A shard either *references* a resource (`binds:
  [database: main]`) or *defines* it (full spec). Across the project: **≤1
  definition per name**; conflicting definitions = a **deterministic error** naming
  both shards + the diverging field.
- **Referenced-but-undefined = error** (not implicit create), to stay
  deterministic; `[soon]` `skali config validate` pre-flights. **Decided** over
  provision-with-defaults / interactive-create.
- **Authority-scoped prune.** Only a resource's *defining* shard (or `server`) can
  remove it; a consumer dropping a reference removes only its binding. Combined
  with the stateful no-auto-prune rule, a vanished definition loses the spec, never
  the data.
- **Rejected:** syncing the full config across repos (re-imposes monorepo coupling
  without the benefits). (`11`.)

## Earlier decisions (from the initial plan)

Carried here so `16` holds only open items; detail in the cited chapters.

- **Declarative reconciler over imperative control.** Desired state in SQLite,
  workers report actual via Docker events, master converges; container crash
  recovery for free; startup resync uses the steady-state path. (`03`.)
- **Zero-downtime rollout = start-new-then-stop-old, health-gated.** No health
  check ⇒ new Instance treated healthy on start, old stopped immediately
  (documented tradeoff). Blue/green/canary `[soon]`. (`03`.)
- **Secrets at rest deferred out of 0.1.0.** Plaintext in SQLite for now (protect
  via host/file perms); AEAD + operator master key in a `[soon]` pass; columns
  hold plaintext-now / ciphertext-later. (`05`/`08`.)
- **Built-in managed `registry:2`** as the default artifact store (core dependency
  like Traefik); images are the rollback substrate so skali owns the lifecycle;
  external registries still supported. (`04`/`10`.)
- **Environments are first-class** between Project and Release; config-driven
  multi-env management in 0.1.0; image **promote** (reuse a digest across envs)
  `[soon]`. (`05`/`11`.)
- **Certificates: HTTP-01 per-host for 0.1.0**; wildcard DNS ≠ wildcard cert;
  DNS-01 wildcard `[soon]`. (`02`.)
- **Local build is a 0.1.0 core feature.** `build.builder` = `auto|local|<node>`;
  build location decoupled from run location; the one exception to "CLI never
  touches the registry." (`04`/`01`.)
- **Build job queue, no broker.** The `builds` table is the queue; master is the
  scheduler (push, single-writer); run-once jobs distinct from the level-triggered
  instance reconciler. (`04`/`05`.)
- **Architecture reporting locked in; native-only matching in 0.1.0**; QEMU
  cross-arch `[future]`. (`04`/`07`.)
- **Async-by-default deploy.** Returns a job id; `--follow` to attach; context
  persisted as a content-addressed blob, GC'd at terminal state. (`09`/`06`/`04`.)
- **Source transport: remote build does *not* require a repo.** local / context-tar
  upload / git-clone (`[soon]`); rejected "force local build when no repo." (`04`.)
- **Unified notification system** (`0.3.x`): one event core + outbox delivery
  engine, pluggable transports (webhook, email, `[future]` mobile push). (`16`/`06`.)
- **App access protection** (`0.2.x`): access policy on a route/environment →
  Traefik middleware; basic-auth first. (`02`.)
- **Opaque server-stored tokens, not JWT** (one master/DB ⇒ instant revocation).
  Node auth = cluster CA + one-time join token + mTLS. (`08`.)
- **Per-environment deploy lock**; different envs of one project deploy
  concurrently. (`05`.)
- **One `.skali` config ↔ one project**; "temporary" is a lifecycle (TTL)
  attribute, not a new entity. (`16`/`15`.)
