# Skali roadmap

This is the plan of record. It lists what skali should be able to do, roughly
in the order it matters, without prescribing how. The code and its comments
describe what exists; anything below may be reworked freely, and nothing
that exists is sacred.

Rule of thumb: production confidence first, then make the console honest,
then features. Everything is dogfooded on a real cluster before it counts.

## Where we are (2026-08-26)

Working and used daily:

- `skali dev`: disposable local k3d platform, build/import, deploy, live run
  tree, log follow, exec, detach, local dev mode with intercepts, auto ports,
  upgrade.
- `skali deploy` / `plan` / `rollback` / `promote` against a remote, stored
  values with `--env-file` staging and `--prune-values`, per-checkout target
  binding, ready summary with route links, per-application build platforms
  on mixed-architecture clusters.
- Applications, Postgres databases (shared CNPG substrate), S3 buckets
  (SeaweedFS), persistent volumes (local-path or Longhorn), routes with
  Traefik IngressRoute, TLS, load-balancing strategy, blue-green rollouts
  by default (rolling and recreate selectable).
- `skali cluster`: install, join (agents and HA servers), init, upgrade,
  status, diagnose, repair, tier, uninstall, addresses, storage-migrate,
  reset-password; macOS via Lima.
- `skali remote` with instance identity pinning; registry token protocol.
- Permissions: one role ladder per project and per environment, promote-only
  protection with recorded bypass, environment priority as PriorityClasses.
- Manual environment backup/restore to an external S3 target, cross-env
  restore, project-wide listing.
- Metrics: cpu/mem per node and per service, storage per node and per
  project (volumes, databases, objects, temporary), in the API and console.
- Web console on real APIs: projects, services, deployments, runs (cancel,
  redeploy, promote), metrics, values, users, nodes, account/2FA; every
  other tab is an honest placeholder.
- Agent skill (`skali skill install`), kept in step with the compiler by
  tests.
- Release plumbing: goreleaser, `install.sh`, published images, prerelease
  handling, CI gating the tag.

No release has been tagged yet; `RELEASE_CHECKLIST.md` tracks what is left
before 0.1.0. Known gaps are written down in
[`docs/limitations.md`](docs/limitations.md).

## 1. Production confidence

The goal of this block: I can put a paying workload on skali and sleep.

- [x] Permission system: one role ladder (`none | read | deploy | maintain |
      admin`) used per project (the default) and per environment (cells and
      a ceiling), locked environments stay listed, promote-only protection
      with an explicit recorded admin bypass, environment priority
      (`normal | high`, instance admins only) rendered as PriorityClasses.
      Model and route classification in
      [`docs/permissions.md`](docs/permissions.md).
- [ ] Tag a first release. The plumbing is in place; what remains is the
      rehearsal on a prerelease tag: `install.sh` on clean machines,
      published images, `skali cluster upgrade` from a released binary.
- [ ] Skali's own state is backed up off-cluster (system database, values
      keys, installer inputs) and a full restore into a fresh install has been
      done at least once. `skali cluster restore` exists as a hidden stub
      until then.
- [ ] Continuous Postgres backup (WAL archiving) to the backup target with
      point-in-time restore, not only manual logical dumps.
- [x] Bucket data has a backup story tested the same way: `skali backup`
      snapshots buckets (and volumes) next to databases, covered by the
      backup e2e suite. Continuous copy-out rides with the WAL item.
- [ ] Registry retention and garbage collection so disk does not grow
      forever. Artifact records carry leases and an eviction path already;
      nothing runs it, and registry blobs are never deleted.
- [ ] Stateful removal: reclaim PersistentVolumeClaims that disappear from
      a manifest and namespaces orphaned outside a purge, as an explicit
      destructive transition. Today both are retained forever.
- [ ] Multi-node production topology on Hetzner (control-plane/edge trio,
      app nodes, db nodes) actually installed and running. Run the never-run
      live checks against it: HA server join, availability tiers,
      diagnose/repair, seaweed replication, edge failover.
- [ ] Rolling `skalid` and the web console on a live cluster without
      dropping in-flight runs or user traffic. Old daemon versions must not
      quietly serve after an upgrade. (Console updates refuse to start
      while runs are in flight, which covers the common case, not a roll
      that begins mid-run.)
- [x] Basic metrics (cpu/mem/disk per node and per service, database and
      bucket usage) exposed in API and console.
- [ ] Minimal alerting: node down, workload crashlooping, disk or quota near
      full, backup failed, certificate not renewing. Delivery can start as
      email or webhook.
- [ ] Sensible defaults for resource requests/limits and a way to see who is
      using what; per-priority defaults, a cap on normal-priority
      consumption, and database placement by environment priority (the
      PriorityClasses exist; the defaults and caps do not).
- [ ] Documented recovery runbook: lost node, lost disk, lost control plane,
      lost skalid database. Each path tried once.
- [ ] Move a real production workload onto skali and leave it there.

## 2. Console catch-up

Make the web console honest: every tab is real or gone.

- [ ] Service logs, environment (resolved values), domains, and deployments
      detail on the existing APIs. Deployments are real; logs, environment,
      and domains are placeholders.
- [ ] Project activity from runs; environment overview with health, active
      revision, pending changes.
- [ ] Dashboard and system pages from observation and node data (what the
      CLI already knows).
- [ ] Backups page (targets, list, trigger, restore).
- [ ] Domains and certificates overview.
- [x] Remove or hide until real: the service graph mock and the access
      tokens control are gone; scaling, alerts, and the rest are honest
      placeholders. Left: the sidebar collapse toast.
- [ ] Web terminal (exec) and rollback from the console. Run cancel,
      redeploy, and promote are done.
- [x] Decided: the console is a status and operations surface; `skali.yaml`
      stays the only source of truth for definitions.

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
- [x] Blue-green rollouts as the default for applications without volumes:
      the new version starts beside the old one, becomes fully ready, then
      takes traffic in one selector switch; a version that never becomes
      ready never serves. Rolling (maxUnavailable/maxSurge) and recreate
      stay selectable, volumes still force recreate and one replica; see
      docs/limitations.md for the capacity and strategy-switch notes.
- [ ] Templates / examples catalog and Compose import.
- [ ] LAN / HTTP-only installation profile for home and office clusters.
- [ ] Notifications (deploy finished, run failed) to chat/webhook.
- [ ] Custom domains with automatic verification and redirects.
- [ ] Autoscaling of applications, and placement/priority policies across
      environments.

## 4. Platform and operations

- [x] Console-driven platform updates: a daily release scan (GitHub
      releases, stable or beta channel), the System / Software update page
      with per-node progress, automatic updates, and the coordinator moving
      every node's hostd and k3s plus the bundle in one operation.
      `skali cluster upgrade` remains the path for legacy installs and
      `skali dev upgrade` for the local platform. Not yet rehearsed on a
      real cluster from a released binary.
- [ ] Version pin bumps as a routine (k3s, CNPG, SeaweedFS, Traefik,
      cert-manager, Longhorn) with the cluster e2e as the gate.
- [ ] Node lifecycle: drain, replace, retire a node without hand surgery;
      includes moving the registry off its node so it can be removed.
- [ ] Multi-edge traffic distribution and DNS guidance.
- [ ] Management-plane HA (skalid itself) once a second cluster or a real
      outage motivates it.
- [ ] Security pass: trusted-proxy handling for client IPs, session and
      login-challenge expiry sweeps, backup archive path validation,
      checksum signing and SBOM for releases, dependency review. (Redaction
      and exec/logs authorization were reviewed in the pre-release audit.)
- [ ] Log retention and shipping story beyond kubelet defaults.

## 5. Developer experience and housekeeping

- [ ] Docs: getting started, manifest reference, operations. The README,
      `docs/limitations.md`, and `docs/development.md` exist; a manifest
      reference and an operations guide do not, and the examples suite is
      not exercised by tests.
- [x] `skali` skill and manifest schema kept in lockstep with the compiler:
      every manifest fence in the skill compiles under test, and the schema
      is generated from the Go types.
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
