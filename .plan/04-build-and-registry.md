# 04 — Build & Registry

## Build ↔ release decoupling

The platform is **image-based**. Two independent verbs with a registry between
them:

- **Build** = (source) → image **pushed to a registry** → an immutable
  `registry/repo@sha256:…` digest. Runs on a `builder` node.
- **Release/Deploy** = (image digest + config + target nodes) → running
  Instances. A node only needs to `docker pull` the digest and run it.

The payoff: **"deploy an existing image" and "build then deploy" are the same
path** — one just skips the build. `skali deploy` from a Dockerfile builds then
releases; a future `skali deploy --image nginx:1.27` is release-only.

## Where the source comes from vs where the image goes

These are two different things and must not be conflated:

- **Source transport (build input)** — how the source reaches whoever builds it.
  **Three paths, and a remote build does *not* require a connected repo:**
  1. **Local build** — no transport; the source is already on the dev machine,
     which builds + pushes a digest (`--builder local`).
  2. **Remote build + context upload** — the client **tars the build context**
     (respecting `.dockerignore`) and uploads it; a builder node builds it. *This
     is the no-repo case* — it works for any directory, builds the exact working
     tree, and is what lets a **weak client use a powerful build server without a
     repo**. (Forcing local-build-when-no-repo would defeat that, so we don't.)
  3. **Remote build + git clone** `[soon]` — when a repo is connected, the builder
     clones the ref; no upload. Efficient for large contexts and the basis of
     push-to-deploy. An add-on, never a requirement.
- **Artifact store (build output):** the builder pushes the resulting image to a
  **registry**. Releases reference the registry digest. The registry **is** the
  artifact store.

So context-in (source) and registry-out (artifact) coexist; the registry is never
the source path.

### Async builds need the context *persisted*, not streamed

Because server-side builds are queued and **deferred** (the assigned builder may be
busy, `04` build queue), an uploaded context can't ride a single live connection —
it must be **stored**:

- The client uploads → the **master persists the context as a content-addressed
  blob** (keyed by the `cli-upload:<hash>` in `builds.source_ref`, `05`), under the
  master data dir.
- The **assigned builder fetches the blob when it picks up the job** (a gRPC fetch
  / signed URL), then builds.
- The blob is **GC'd once the build reaches a terminal state** (success or failure)
  — the durable artifact is the image in the registry; the context is ephemeral.

(For path 3, git-clone, there is no blob — the builder clones the ref directly.)

## Built-in managed registry (default); external optional

> **Decision:** 0.1.0 ships a **built-in registry** — a `registry:2` container
> that `skalid` manages end-to-end as a **core dependency alongside Traefik**
> (`10`). It is the default artifact store. Connecting an **external** registry
> stays supported through the same `Registry` abstraction, for operators who want
> S3-backed/shared infra or a separate "release" registry.

Why built-in is the default rather than "bring your own":

- **skali owns the image lifecycle.** Images are not transient — a Release pins a
  digest and **rollback re-releases a prior digest** (`03`), so build images *are*
  your rollback history. Owning the registry lets skali retain the last N per
  environment and GC the rest on its own schedule, instead of leaving cruft in (or
  reaching into) someone else's registry.
- **Auth folds into the cluster identity.** Builders push and workers pull
  server-side (the CLI never touches the registry — builds are server-side, `06`).
  An in-cluster registry authenticates those nodes with the **mTLS identity from
  enrollment** (`08`) — no second credential set to store and distribute.
- **Zero-config single-node.** "Install skalid, run `start`" yields a working
  registry with no external dependency.

The **"always through the registry"** model is unchanged and is what keeps single-
and multi-node identical: even on one node, build pushes the image and deploy
pulls it (a fast, loopback no-op pull when the image is already local). Whether the
registry is built-in or external changes nothing downstream.

### Registry options (uniform management)

- **Built-in** (0.1.0 default): a managed `registry:2` container on the master,
  storage under the master's data dir, authenticated via the cluster CA, GC driven
  by skali's retention policy. Stored as a `Registry` record, `kind = builtin`.
- **External / connected** (optional): operator connects an existing registry
  (their own, Docker Hub / GHCR / Harbor / ECR / …) as a `Registry` record with
  credentials — e.g. as a dedicated **release registry** (see promotion below).
- **All flows work the same** regardless of which registry backs a Project or
  Environment — that uniformity is a hard requirement.

## Promotion & cross-registry export

Because a Release pins a digest and env is injected at **runtime**, the *same
image* normally serves every Environment. **Promotion** exploits this:

- `skali promote staging production` (`[soon]`) takes staging's current digest,
  creates a **new Release in production** = that digest + production's env/config
  snapshot, and rolls it out — **no rebuild** (the `promoted_from` column records
  the lineage, `05`). Production jumps from its old digest to staging's validated
  one in a single release, which is naturally "all the commits since the last
  release."
- **[future] cross-registry export:** when the target Environment is backed by a
  *different* registry (e.g. a separate **release registry**), promotion **copies
  the blob by digest** into that registry, then releases. This turns skali into a
  fuller CI/CD pipeline — every commit builds to a staging registry; tagging a
  commit promotes that already-built image into the release registry — **without
  a second build**.

> **Caveat — build-baked config breaks image reuse.** Promotion-without-rebuild
> only works when per-environment differences are injected at **runtime**. Config
> **baked at build time** (frontend bundlers inlining `NEXT_PUBLIC_*`, `VITE_*`,
> `REACT_APP_*`; anything compiled in) makes images env-specific — a staging build
> literally contains `staging.example.com`. Such Projects set `build_per_env`
> (`05`): skali builds a separate image per Environment (correct, but no "saved
> build"). Default is build-once-then-promote.

## Build execution: per-host capability + selectable location

Build cost and run cost are **decoupled**: a 4–8 core VPS runs apps fine but
builds painfully slowly (minutes to tens of minutes), while a powerful workstation
builds the same image in seconds. skali therefore **never forces builds onto the
machines that run the app** — build *location* is a first-class, selectable choice.

- **Builder is a per-host capability (a role).** Grant the `builder` role only to
  capable nodes (`skalid enroll --roles builder`, or via node admin); a weak
  `worker` VPS is never asked to build. "Set build capability per host" = assigning
  the role.
- **Local build is a first-class location.** The `skali` CLI can build on the
  **developer's own machine** (e.g. an M2 Ultra) via its local Docker/BuildKit,
  then push the image to the skali registry. From the digest onward the pipeline is
  identical — skalid doesn't build it again.
- **Selection** (`build.builder` in `.skali/config.yaml`, committed; override with
  `--builder`):
  - `auto` *(default)* → use a healthy `builder` node if one exists, **else fall
    back to local**. So with **no build host, local is automatic**.
  - `local` → always build on this machine (the weak-cloud / strong-laptop case).
  - a node name (`build-server-01`) → build on that specific builder.
  - When more than one option exists you pick from the list
    (`build-server-01`, `build-server-02`, `local`).

This is a **0.1.0 core feature**, not a `[soon]` nicety — it's the main reason to
choose skali over building on weak cloud infra. Even single-node users set
`build.builder: local` to build on their laptop and ship to the cloud node.

### One build routine, two drivers

The BuildKit build-and-push routine lives in a shared `internal/builder` package
(`14`), used by **both** binaries so the behavior is identical wherever it runs:

- **skalid (builder node):** the master calls `Executor.BuildImage` on a builder
  node (Local, or Remote over gRPC for a dedicated build server); logs stream
  builder→master→client SSE.
- **skali (local build):** the CLI runs the *same* routine against local Docker,
  prints build logs **directly** (no SSE — it's local), pushes the digest, then
  hits the release-from-digest API. Only the rollout phase streams via SSE.

### Local build path (what `--builder local` does)

```
1. skali builds locally (BuildKit) from the context in place
   — NO context tarball upload; the source is already on this machine
2. skali pushes the image to the skali registry
   — needs a client-reachable, authenticated registry endpoint + a short-lived
     scoped push token the master mints from the user token
     (the one place the CLI touches the registry — see the invariant note in 01)
3. skali → POST /projects/{id}/deploys?env=… { image_digest }   (release-from-digest)
4. master creates the Release and reconciles (health-gated rollout, 03);
   skali streams the rollout via SSE
```

So local build **reuses the "deploy an existing image" path** (`06`): build
location varies, but **everything still goes through the registry**, and the
release/run half is byte-identical to a server-side build. A bonus: it skips the
context upload entirely (only image layers move, and those dedupe in the registry).

> **Wrinkle to settle in implementation — registry reachability.** A dev machine is
> a *client*, not necessarily on the cluster's private network, so for local push
> the built-in registry must be **client-reachable**: an authenticated HTTPS
> endpoint (e.g. exposed via the edge as `registry.<cluster-domain>`) in addition
> to the private-net endpoint nodes use. Installs that don't want to expose the
> registry can instead **relay the push through the master** (CLI uploads the image
> over REST; master pushes on the private net) — slower, but the registry stays
> private. Recommend the authenticated endpoint; keep relay as a fallback.

### Build inputs / config (from `.skali`, see `11`)

- `dockerfile` path (default `./Dockerfile`), build `context` dir.
- **build args** and **build-time secrets** via BuildKit `--secret` (`[soon]`) so
  npm tokens / private repo creds don't bake into layers.
- target image name derived from project (`<registry>/<project-slug>`), tag from a
  build id / git sha; the **digest** is what's pinned in the Release.

## Build → Release pipeline (server-side build — `--builder auto`/`<node>`)

This is the path when a **builder node** does the build (the local-build path is
above; both converge at the registry digest):

```
1. client tars context ──▶ POST /projects/{id}/deploys?env= (multipart upload)
2. master persists the context blob + records a Build (status=queued);
   returns { deploy_id, build_id } ──▶ CLI prints the job id and returns (ASYNC)
3. scheduler assigns a free builder, dispatches BuildImage (local executor in
   0.1.0); the builder fetches the context blob
4. builder: docker build (BuildKit) ──▶ push to registry ──▶ returns digest
   (logs stream master←builder, and to any --follow client via SSE)
5. master records the digest on the Build (status=succeeded); context blob GC'd
6. master creates a Release for the target Environment (digest + that env's
   snapshot/placement) and marks it that environment's desired version
7. reconciler converges nodes to the new Release (health-gated rollout, see 03)
8. job reaches done — visible via `skali builds` / SSE. (The project id was written
   to .skali at project creation, not here.)
```

A failed build (step 4) leaves the previous Release serving untouched; the job is
marked failed and surfaces in `skali builds ls`.

## Build queue & scheduling

Server-side builds run through an **async job queue**, so deploys never block on
build capacity and builds distribute across builder nodes. Crucially, this needs
**no message broker** — it would break skali's self-contained / SQLite-source-of-
truth ethos:

- **The queue is the `builds` table** (`05`). A deploy enqueues a row
  (`status=queued`); the states (`queued→building→pushing→succeeded|failed`) *are*
  the queue.
- **The master is the scheduler** (single writer). It picks the next eligible
  `queued` build, assigns a builder, dispatches `Executor.BuildImage` over gRPC,
  and updates state. **Push model** — not worker-pull — because only the master
  writes the DB; a pull queue would violate the single-writer invariant.
- **Builds are a job queue, not the run reconciler.** Distinct semantics: a build
  is a **run-once task** (retry on failure, bounded `attempts`), whereas instances
  are **level-triggered desired state** (converge forever). Same owner (master),
  same transport (gRPC), different lifecycle — kept separate on purpose (`03`).

Scheduling policy:

- **Concurrency caps** — `nodes.build_concurrency` (`05`) bounds concurrent builds
  per builder; matters even single-node so a weak host isn't thrashed. Excess jobs
  wait in `queued` (free backpressure; the client sees "waiting for a builder").
- **Cache-sticky assignment** — prefer the builder that last built a project, so
  BuildKit layer cache stays warm (a big real-world speedup).
- **Requeue on builder death/timeout** — bump `attempts`, re-dispatch; after a
  bound, mark `failed` (old Release keeps serving).
- **Local builds bypass the queue** — they're client-side (`build_location=local`)
  and arrive as release-from-digest; they get a `builds` row for history but are
  never scheduled.

**0.1.0 ships the degenerate queue:** one eligible builder (the master node) +
concurrency cap + these states. **Distribution across multiple builders is the
same model with more eligible workers** — purely additive, `[soon]`.

## Architecture awareness (cross-arch builds)

Container images are **platform-specific**, so build placement must respect
architecture — concretely: an arm64 workstation (e.g. an M2 Ultra) builds *arm64*
images by default, which **won't run** on amd64 cloud nodes.

- **Every host reports its architecture** (`amd64`/`arm64`) to the master — nodes
  via Heartbeat (`07`, stored in `nodes.arch`), and the `skali` CLI knows its own
  for local builds. So the master always knows which builders and runners are which
  arch.
- **0.1.0: native matching only.** A build's `target_platform` must be produced by
  a builder of that arch, and a Release is placed only on **matching-arch** nodes.
  On a mismatch (e.g. a local arm64 build but only amd64 runners) the master
  **errors clearly** rather than shipping an unrunnable image. Homogeneous clusters
  (all one arch) never hit this.
- **[future] emulated cross-build (QEMU/buildx `--platform`).** A powerful arm64
  builder emulating amd64 is often *magnitudes* faster than a weak amd64 node
  building natively — so emulation is a deliberate lever to keep, not just a
  fallback. **[future]** multi-arch manifest builds (push amd64+arm64 under one
  tag) for mixed-arch clusters.

## Image lifecycle / garbage collection [soon]

Old images accumulate in the registry and on nodes. Because the registry is now
**skali-owned** (built-in by default), skali drives the policy directly:

- keep the last N Releases' images per **Environment** (so each env keeps its own
  rollback history); a digest still referenced by *any* environment's retained
  Releases is never pruned.
- prune unreferenced digests from the registry and from node-local Docker.
- registry GC: delete the dangling manifests via the registry API, then run the
  `registry:2` blob GC pass on a schedule (briefly read-only) — orchestrated by
  skalid the same way it manages the container's lifecycle (`10`).
- node-local `docker image prune` for digests no longer referenced by any
  desired Instance.

Retention-by-policy is the first slice; the read-only blob sweep can follow.
Either way the `Build`/`Release` records retain digests so GC can reason about
what is still referenced (and so rollback/promotion still works).
