# Skali roadmap

This is the plan of record. It lists what skali should be able to do, roughly
in the order it matters, without prescribing how. The earlier architecture
plans (`REWORK_V2.md`, `.plan/`, `docs/transcripts/`) were removed on
2026-08-19 and exist only in git history; the code and its comments are the description of what exists.
Anything below may be reworked freely; nothing that exists is sacred.

Rule of thumb: production confidence first, then make the console honest,
then features. Everything is dogfooded on a real cluster before it counts.

## Where we are (2026-08-19)

Working and used daily:

- `skali dev`: disposable local k3d platform, build/import, deploy, live run
  tree, log follow, exec, detach, local dev mode with intercepts, auto ports,
  upgrade.
- `skali deploy` / `plan` / `rollback` / `promote` against a remote, stored
  values with `--env-file` staging and `--prune-values`, per-checkout target
  binding, ready summary with route links.
- Applications, Postgres databases (shared CNPG substrate), S3 buckets
  (SeaweedFS), routes with Traefik IngressRoute, TLS, load-balancing
  strategy.
- `skali cluster`: install, join (agents and HA servers), init, upgrade,
  status, diagnose, repair, tier, uninstall, addresses; macOS via Lima.
- `skali remote` with instance identity pinning; registry token protocol.
- Manual environment backup/restore to an external S3 target, cross-env
  restore, project-wide listing.
- Web console on real APIs: projects, services, deployments, runs, values,
  users, nodes, account/2FA.
- Agent skill (`skali skill install`).

One single-node installation runs a dev build in daily use. No release has
been tagged yet.

## 1. Production confidence

The goal of this block: I can put a paying workload on skali and sleep.

- [ ] Permission system: one role ladder (`none | read | deploy | maintain |
      admin`) used per project (the default) and per environment (cells and
      a ceiling), locked environments stay listed, promote-only protection
      with an explicit recorded admin bypass, environment priority
      (`normal | high`, instance admins only) rendered as PriorityClasses.
      Model and route classification in
      [`docs/permissions.md`](docs/permissions.md).
- [ ] Tag a first release. Prove goreleaser, `install.sh`, published images,
      and `skali cluster upgrade` from a released binary end to end.
- [ ] Skali's own state is backed up off-cluster (system database, values
      keys, installer inputs) and a full restore into a fresh install has been
      done at least once.
- [ ] Continuous Postgres backup (WAL archiving) to the backup target with
      point-in-time restore, not only manual logical dumps.
- [ ] Bucket data has a backup story (replication or copy-out) that is
      tested the same way.
- [ ] Registry retention and garbage collection so disk does not grow
      forever.
- [ ] Multi-node production topology on Hetzner (control-plane/edge trio,
      app nodes, db nodes) actually installed and running. Run the never-run
      live checks against it: HA server join, availability tiers,
      diagnose/repair, seaweed replication, edge failover.
- [ ] Rolling `skalid` and the web console on a live cluster without
      dropping in-flight runs or user traffic. Old daemon versions must not
      quietly serve after an upgrade.
- [ ] Basic metrics (cpu/mem/disk per node and per service, database and
      bucket usage) exposed in API and console.
- [ ] Minimal alerting: node down, workload crashlooping, disk or quota near
      full, backup failed, certificate not renewing. Delivery can start as
      email or webhook.
- [ ] Sensible defaults for resource requests/limits and a way to see who is
      using what; per-priority defaults, a cap on normal-priority
      consumption, and database placement by environment priority (the
      `priority` field and the PriorityClasses `skali-critical`,
      `skali-high`, `skali-normal` come with the permission system).
- [ ] Documented recovery runbook: lost node, lost disk, lost control plane,
      lost skalid database. Each path tried once.
- [ ] Move a real production workload onto skali and leave it there.

## 2. Console catch-up

Make the web console honest: every tab is real or gone.

- [ ] Service logs, environment (resolved values), domains, and deployments
      detail on the existing APIs.
- [ ] Project activity from runs; environment overview with health, active
      revision, pending changes.
- [ ] Dashboard and system pages from observation and node data (what the
      CLI already knows).
- [ ] Backups page (targets, list, trigger, restore).
- [ ] Domains and certificates overview.
- [ ] Remove or hide until real: scaling tab, alerts tab, service graph mock,
      "coming soon" toasts, access tokens link.
- [ ] Web terminal (exec) and run cancel/rollback from the console.
- [ ] Decide whether the console ever edits definitions or stays a
      status/operations surface with `skali.yaml` as the only source of truth.
      Current lean: status/operations only.

## 3. Product features

Ordered loosely by how often I have wanted them.

- [ ] Push-to-deploy: git integration and a managed builder so a push
      deploys without a laptop CLI.
- [ ] API/CI tokens (membership already exists, tokens become subjects).
- [ ] Credential rotation for databases and buckets.
- [ ] Scheduled jobs (cron) and one-off commands per application.
- [ ] Sidecars or multiple processes per application, if a real app needs
      it.
- [ ] Bucket policies: public-read, versioning, lifecycle, object quotas.
- [ ] Environment cloning (staging from production data).
- [ ] Preview environments per branch.
- [ ] External access to databases (allowlisted TCP) for tooling.
- [ ] More database engines (Valkey/Redis first, then whatever a real
      project needs).
- [x] Persistent volumes for applications where a stateless port is not
      realistic: configurable storage driver, local-path by default and
      Longhorn opt-in (enforced sizes, replication, node-loss survival);
      the driver enum is the seam for provider-native drivers like
      hcloud-csi later; see docs/storage.md.
- [ ] Templates / examples catalog and Compose import.
- [ ] LAN / HTTP-only installation profile for home and office clusters.
- [ ] Notifications (deploy finished, run failed) to chat/webhook.
- [ ] Custom domains with automatic verification and redirects.
- [ ] Autoscaling of applications, and placement/priority policies across
      environments.

## 4. Platform and operations

- [ ] Version pin bumps as a routine (k3s, CNPG, SeaweedFS, Traefik,
      cert-manager, Longhorn) with the cluster e2e as the gate.
- [ ] Node lifecycle: drain, replace, retire a node without hand surgery.
- [ ] Multi-edge traffic distribution and DNS guidance.
- [ ] Management-plane HA (skalid itself) once a second cluster or a real
      outage motivates it.
- [ ] Security pass: redaction audit, dependency review, session and token
      hygiene, exec/logs authorization review.
- [ ] Log retention and shipping story beyond kubelet defaults.

## 5. Developer experience and housekeeping

- [ ] Docs: getting started, manifest reference, operations, examples suite
      that is exercised by tests.
- [ ] `skali` skill and manifest schema kept in lockstep with the compiler.
- [ ] Simplify what grew crooked: revisit CLI command grouping, flag names,
      and error wording once the console catch-up shows what is actually
      used.
- [ ] Reduce e2e wall-clock and flakes; keep unit, live, dev, and cluster
      suites runnable on a laptop.

## Explicitly not planned

- Installing onto a Kubernetes cluster skali does not own.
- Supporting the pre-v2 schema or any migration from it.
- A second authoritative state plane. New features are service modules,
  policies, run kinds, or client workflows.
