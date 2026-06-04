# 15 — Roadmap

Versioned milestones. The architecture is designed so each later item slots in
without rework (interfaces, roles, image-based deploys, derived routing config).

## 0.1.0 — the canonical flow (single node)

The acceptance scenario from `12`:

- [ ] `skalid start` on one node (master+edge+worker+builder), self-migrating DB.
- [ ] Cluster CA generated; mTLS gRPC server up (even if only the local
      short-circuit is exercised).
- [ ] REST API (chi) + embedded web UI; opaque-token auth (`auth login`).
- [ ] Built-in managed `registry:2` (skalid-supervised, like Traefik) as the
      default; builds push, deploys pull. External registry connectable as an option.
- [ ] Environments: schema + implicit default `production`, plus **config-driven
      management** — `environments:` block, `skali env create` (edits config),
      `skali apply`/`deploy` reconciles topology to the server (additive +
      drift-correct; `--prune` gates deletes).
- [ ] Route uniqueness `(host, listen_port, path_prefix)` enforced cluster-wide.
- [ ] `skali` CLI: `auth login`, `init`, `deploy` (huh wizard), contexts.
- [ ] Build context upload → server-side BuildKit build → push to registry.
- [ ] **Local build (core):** `--builder local` / `build.builder` builds on the dev
      machine and pushes to the registry; `auto` falls back to local when no
      `builder` node exists. Per-host `builder` role = build capability. Needs a
      client-reachable authenticated registry endpoint + master-minted scoped push
      tokens, and the release-from-digest deploy mode.
- [ ] **Build job queue** (SQLite-backed, master-scheduled, no broker): per-builder
      concurrency cap, backpressure, requeue on failure. Degenerate single-builder
      form in 0.1.0.
- [ ] **Async-by-default deploy:** `skali deploy` returns a job id immediately;
      `skali builds ls`/`status`/`logs` + `--follow` to attach. Context uploads
      persisted as content-addressed, GC'd blobs so queued/remote builds work
      without a repo.
- [ ] **Architecture-aware** scheduling + placement: nodes report `arch`; native
      build/run matching with a clear error on mismatch.
- [ ] Release + reconciler + health-gated rollout; routes via Traefik labels.
- [ ] Edge Traefik with ACME HTTP-01; wildcard DNS routing.
- [ ] Container network isolation + `DOCKER-USER` firewalling.
- [ ] `.skali/` config written back with project id.
- [ ] Rollback (`releases rollback`) — cheap given digest-pinned releases.

## 0.2.x — multi-node & robustness

- [ ] Real second node: `skalid enroll` (join token + mTLS), `RemoteExecutor`.
- [ ] Edge HTTP-provider routing to remote nodes; cross-node replica LB.
- [ ] Cross-node build dispatch to a dedicated `builder` node over gRPC, two-hop
      log streaming — the remote-node complement to 0.1.0 local build.
- [ ] Distributed build scheduling across multiple builders: cache-sticky
      assignment, per-arch builder pools, load-aware dispatch.
- [ ] **Git-clone build source:** builder clones a connected repo instead of a
      context upload (efficient for big contexts; the basis for push-to-deploy).
- [ ] `skali builds cancel` (cancel a queued/running job); push notifications on
      job completion.
- [ ] RBAC (owner/admin/deployer/viewer, per-project scope).
- [ ] **App access protection** (keep a staging site private for customer review
      without making it public): attach an **access policy to a route/environment**,
      rendered as a **Traefik middleware**. **Basic-auth first** (per-route/env
      user:pass) — simplest thing that works. `[future]`: IP allowlist, forward-auth
      with a custom login / SSO, signed preview links.
- [ ] **Image promotion** across environments (`skali promote`) — reuse the
      validated digest with the target env's config, no rebuild.
- [ ] Environment niceties: web-UI env switcher, `skali env vars pull`, drift
      reporting in `skali env list`.
- [ ] **Repo auto-detection:** on `init`/`deploy`, read `git remote origin` and, if
      a GitHub app is connected, **offer** to link the matching repo + pre-fill the
      wizard (detect-and-suggest, never silent-link).
- [ ] Resource limits (CPU/mem, upload size) per environment.
- [ ] **Secrets at rest** (env, CA key, registry creds): AEAD + operator master key.
- [ ] Image/registry garbage collection (skali-owned retention + blob GC).
- [ ] Compose support: committed `compose.yaml` → multi-service projects.
- [ ] Web UI: rollback, scaling, build history.

## 0.3.x — deploy ergonomics & platform

- [ ] **GitHub push-to-deploy** (incoming webhooks on the REST plane; reads `.skali`).
- [ ] **Unified notification system** — one event core + delivery engine with
      **pluggable transports**. Events (`deploy.succeeded`/`failed`, `build.failed`,
      `rollout.failed`, `rollback.performed`, `instance.unhealthy`,
      `node.unreachable`, `cert.renewal_failed`, …) route to subscriptions scoped
      cluster/project/environment. Shared **outbox on SQLite** (event + delivery
      rows committed with the state change; master dispatcher retries w/ backoff,
      auto-pause, delivery log, test ping) — same no-broker pattern as the build
      queue. Transports plug into that one engine:
  - **Webhook** — HMAC-signed JSON (`X-Skali-Signature-256`); the GitHub-webhook-
    style outgoing hook (PM tool, Slack, an AI agent on failure).
  - **Email** — needs a cluster SMTP / email-provider setting (SES/Postmark/
    Resend/…).
  - **[future]** mobile push (APNs/FCM), native Slack/Discord, etc.
  Generalizes the `[soon]` CLI job-completion notifications (they become one
  transport). Depends only on the event stream existing, so it could move earlier.
- [ ] **Cross-registry export on promote** — copy the validated digest into a
      separate external "release registry"; turns promote into a full CI/CD step
      (commit → staging registry, tag → release registry) with no extra build.
- [ ] Build pipeline (stages, caching, build secrets via BuildKit `--secret`).
- [ ] **Emulated cross-arch builds** (QEMU / buildx `--platform`): a powerful arm64
      builder can produce amd64 (often far faster than a weak amd64 native), and
      multi-arch manifest builds (amd64+arm64 under one tag) for mixed clusters.
- [ ] DNS-01 wildcard certificates behind a pluggable DNS-provider integration.
- [ ] Per-host metrics / basic dashboards; Prometheus endpoint.
- [ ] Persistent volumes / bind storage management for stateful services.
- [ ] Scheduled jobs / one-off tasks (run a container to completion).

## Later / exploratory [future]

- [ ] **Master HA** (the current SPOF): litestream → replication → consensus, or
      a promote-able standby. See `16`.
- [ ] Dead-node rescheduling / failure-aware placement.
- [ ] **Ephemeral lifecycle (TTL + auto-expire)** as a property applicable to a
      **project** (quick throwaway of new code) or an **environment** (preview of a
      known app). Preview environments get a generated subdomain and are *unmanaged*
      (server-side only, not in committed config, never pruned by `apply`), so they
      slot into the declarative model for free. PR-driven previews via the GitHub
      app build on this + repo auto-detection. (Identity rule: one `.skali` config ↔
      one project; "temporary" is a lifecycle attribute, not a new entity type.)
- [ ] Optional data-plane mTLS between proxies (reuse the CA).
- [ ] Alternative backends behind the `Executor`/proxy abstractions: Podman,
      Kubernetes, cloud (AWS/etc.).
- [ ] Built-in nameserver (auto-manage DNS records; coarse geo-steering — never
      the primary LB).
- [ ] External secret providers (Vault, cloud KMS) behind the secrets interface.
- [ ] Cloud-provider LB integration as an optional *front* layer for edge HA
      (e.g. Hetzner LB / floating IP).
- [ ] Multi-tenant / team accounts, audit log, SSO/OAuth login.
- [ ] OS keychain integration for the CLI token.
- [ ] **Mobile app (iOS/Android)** — a GitHub-app-style client over the REST API:
      at-a-glance project/environment status, **push notifications** (the mobile-push
      notification transport, APNs/FCM), and simple controls (restart / stop /
      redeploy / rollback). Reuses the REST API + notification system; adds device
      registration. No new control surface — it's another client.

## Design guardrails that keep this open-ended

- Everything node-acting goes through **`Executor`** → new backends are new impls.
- Deploys are **image-based** → registries, push-to-deploy, and "run any image"
  share one path.
- Routing config is **derived from the DB** → more edges/proxies need no new code.
- Auth **token type is decoupled from acquisition** → browser/OAuth login later
  is additive.
- Node behavior is **role composition** → scaling roles across nodes is config.
