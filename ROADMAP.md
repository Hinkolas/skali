# Skali roadmap

This is the plan of record. It lists what skali should be able to do, roughly
in the order it matters, without prescribing how. The code and its comments
describe what exists; anything below may be reworked freely, and nothing
that exists is sacred.

Rule of thumb: production confidence first, then make the Studio honest,
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
- Manual and scheduled environment backup/restore to an external S3 target
  with per-environment schedules and retention, cross-env restore,
  project-wide listing, and snapshot deletion.
- Metrics: cpu/mem per node and per service, storage per node and per
  project (volumes, databases, objects, temporary), in the API and Studio.
- Studio on real APIs: projects, services, deployments, runs (cancel,
  redeploy, promote), metrics, values, users, nodes, account/2FA; every
  other tab is an honest placeholder.
- Agent skill (`skali skill install`, references served at the target's
  release by `skali skill read`), kept in step with the compiler by tests.
- Release plumbing: goreleaser, `install.sh`, published images, prerelease
  handling, CI gating the tag. [skali.dev](https://skali.dev) serves the
  installer and the editor schemas.

v0.1.0 is in release candidates (latest published: `v0.1.0-rc.10`);
[`v0.1.0-rc.11`](docs/release-notes-rc.11.md) is prepared as the final RC.
No stable release exists yet. Known gaps are written down in
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
- [ ] Release v0.1.0 as the first stable version. Prereleases are
      published; before the stable tag (`task release:tag`, never `git tag`
      by hand), rehearse the release end to end from released binaries:
      `install.sh` on a clean Linux server and a clean Mac, `sudo skali
      cluster` through init, `skali dev` outside the repo, `skali upgrade`
      and `skali cluster upgrade` from the previous release, a `skali
      cluster join` with no local `skali-hostd`, `skali cluster
      reset-password`, and a Studio software update between two releases.
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
- [ ] Replicated metadata for the object store: the bucket directories sit
      in the shared pool at the `single` tier, so a three-node fleet whose
      store survives a node loss still loses its buckets with that one
      Postgres pod. Raise the metadata claim's tier with the fleet.
- [ ] Multi-node production topology on Hetzner (control-plane/edge trio,
      app nodes, db nodes) actually installed and running. Run the never-run
      live checks against it: HA server join, availability tiers,
      diagnose/repair, seaweed replication, edge failover.
- [ ] Rolling `skalid` and the Studio on a live cluster without
      dropping in-flight runs or user traffic. Old daemon versions must not
      quietly serve after an upgrade. (Studio updates refuse to start
      while runs are in flight, which covers the common case, not a roll
      that begins mid-run.)
- [x] Basic metrics (cpu/mem/disk per node and per service, database and
      bucket usage) exposed in API and Studio.
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

## 2. Studio catch-up

Make the Studio honest: every tab is real or gone.

- [ ] Service logs, environment (resolved values), domains, and deployments
      detail on the existing APIs. Deployments are real; logs, environment,
      and domains are placeholders.
- [ ] Project activity from runs; environment overview with health, active
      revision, pending changes.
- [ ] Dashboard and system pages from observation and node data (what the
      CLI already knows).
- [x] Backups page (targets, list, trigger, restore, delete).
- [ ] Domains and certificates overview.
- [x] Remove or hide until real: the service graph mock and the access
      tokens control are gone; scaling, alerts, and the rest are honest
      placeholders. Left: the sidebar collapse toast.
- [ ] Web terminal (exec) and rollback from the Studio. Run cancel,
      redeploy, and promote are done.
- [x] Decided: the Studio is a status and operations surface; `skali.yaml`
      stays the only source of truth for definitions.

## 3. Product features

Ordered loosely by how often I have wanted them.

- [ ] Push-to-deploy: git integration and a managed builder so a push
      deploys without a laptop CLI.
- [ ] API/CI tokens (membership already exists, tokens become subjects).
- [x] Credential rotation on demand (#78): `skali bucket rotate` and
      `skali database rotate` (a database alternates login roles under
      an owner role that never logs in). A rotation schedule is a
      follow-up.
- [ ] Scheduled jobs (cron) and one-off commands per application.
- [ ] Sidecars or multiple processes per application, if a real app needs
      it.
- [ ] Bucket policies: public-read, versioning, object quotas (per-bucket
      CORS and abandoned-upload cleanup landed with #68).
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
      Buckets have their own hostnames (`buckets.<key>.route`, #69) and the
      installation-wide S3 endpoint is gone; a bucket sharing an
      application's hostname under a path follows.
      Verification half done (2026-09-16): every deploy probes whether a
      route's domain reaches this installation and defers the certificate
      until it does (docs/limitations.md, "Routes whose domain does not
      point here yet"); redirect domains remain open.
- [ ] Autoscaling of applications, and placement/priority policies across
      environments.

## 4. Platform and operations

- [ ] Change cluster settings without repeating first-time initialization.
      Observed on kilohertz with alpha.6 (2026-09-10): changing the build
      preference with `skali cluster init --platform-preference
      linux/arm64,linux/amd64` reapplies the bundle and asks for admin
      credentials even though an admin already exists. Skip the bootstrap
      prompt and account-creation job when an admin is present; distinguish
      a failed lookup from an installation with no admin. Add a dedicated
      cluster configuration command (name to be decided) for settings such
      as platform preference, with validation, reviewable changes, and
      persistence through upgrades. Acceptance: changing the preference on
      an initialized cluster requires no bootstrap credentials, creates no
      user, preserves existing accounts/passwords and unrelated settings,
      and affects subsequent deployments; fresh installs still bootstrap
      their first admin.
- [x] Studio-driven platform updates: a daily release scan (GitHub
      releases, stable or beta channel), the System / Software update page
      with per-node progress, automatic updates, and the coordinator moving
      every node's hostd and k3s plus the bundle in one operation.
      `skali cluster upgrade` remains the path for legacy installs; the
      local platform follows the skali release that runs it, one cluster
      per release. Not yet rehearsed on a real cluster from a released
      binary.
- [ ] Version pin bumps as a routine (k3s, CNPG, SeaweedFS, Traefik,
      cert-manager, Longhorn) with the cluster e2e as the gate.
- [ ] Node lifecycle: drain, replace, retire a node without hand surgery;
      includes moving the registry off its node so it can be removed.
- [ ] Multi-edge traffic distribution and DNS guidance.
- [ ] Management-plane HA (skalid itself) once a second cluster or a real
      outage motivates it.
- [ ] Security pass: checksum signing (cosign or gpg), an SBOM, and
      reproducible builds (`mod_timestamp`) for releases; only load `.env`
      in `skalid serve` for dev builds (`internal/config/config.go`); a
      conscious password policy beyond the 8-character minimum; dependency
      review. (Trusted-proxy handling, expiry sweeps, backup archive path
      validation, redaction, and exec/logs authorization are done.)
- [ ] Bump the `alpine:3.21` base of the skalid images (EOL around
      November 2026) and pin it by digest.
- [ ] Release downloads without github.com: Studio updates fetch
      `skali-hostd` and k3s from github.com on every node and scan
      api.github.com. Document the egress requirement and the mirror
      settings (`SKALI_RELEASE_BASE`, `SKALI_UPDATE_FEED_URL`).
- [ ] Log retention and shipping story beyond kubelet defaults.

## 5. Developer experience and housekeeping

- [ ] Docs: getting started, manifest reference, operations. The README,
      `docs/limitations.md`, and `docs/development.md` exist; a manifest
      reference and an operations guide do not, and the examples suite is
      not exercised by tests.
- [x] `skali` skill and manifest schema kept in lockstep with the compiler:
      every manifest fence in the skill's served references compiles under
      test, and the schema is generated from the Go types.
- [ ] Simplify what grew crooked: revisit CLI command grouping, flag names,
      and error wording once the Studio catch-up shows what is actually
      used.
- [ ] Reduce e2e wall-clock and flakes; keep unit, live, dev, and cluster
      suites runnable on a laptop.
- [ ] Prebaked dev node image for a faster first `skali dev`: a
      `skali-dev-node` image built from the pinned `rancher/k3s` with an
      airgap tarball of the k3s built-ins, platform images, and released
      skalid, auto-imported on first boot. Images only, never booted
      cluster state; needs multi-arch builds, a CI rebuild per release, and
      a seam for the `localdev.K3sImage == installer.K3sVersion` drift test.
- [ ] Small cleanups: add `/token` and `/openapi.yaml` to
      `api/openapi.yaml`; replace personal fixtures (`skali.khz.dev`, the
      `nhinke` user) in tests with example.com-style values; drop the
      private-repository `GITHUB_TOKEN` wording from `install.sh`; align the
      `studio/package.json` version with the release tag.

## Explicitly not planned

- Installing onto a Kubernetes cluster skali does not own.
- Supporting the pre-v2 schema or any migration from it.
- A second authoritative state plane. New features are service modules,
  policies, run kinds, or client workflows.
