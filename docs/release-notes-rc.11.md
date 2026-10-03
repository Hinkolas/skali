# v0.1.0-rc.11 — final release candidate (draft)

This candidate gathers the changes since `v0.1.0-rc.10` and completes the
remaining small release fixes. It is prepared for publication; no tag or release
has been published from this branch.

## Changes

- Buckets publish through their own hostnames. Metadata survives backup and
  restore; restore fencing blocks application credentials and presigned URLs
  while data is rewritten. CORS, quotas, abandoned multipart cleanup and
  object-store health and replication now reconcile with the fleet.
- Environments deny ingress from other environments by default. Managed
  PostgreSQL and S3 admit claim holders and required platform peers. The first
  connection waits out NetworkPolicy registration rather than racing it.
- Database and bucket credentials can rotate with a bounded overlap window.
  Managed PostgreSQL supports `pgvector`.
- Bucket backups and restores copy concurrently with retries and batched
  deletes. Run notes survive successful worker completion. Restores fail and
  retain their fence when snapshot objects are missing or the object count
  disagrees with the manifest.
- `skali env rename <environment> <new-name>` and Studio's environment settings
  preserve identity, data, backups and checkout bindings. Promotion rules follow
  renames atomically; old names stay reserved aliases while the environment
  exists. New backup directories use environment UUIDs, preventing name reuse
  from inheriting a deleted environment's snapshots. Migration `00012` preserves
  existing directories and accepted operations.
- Studio dependencies resolve the outstanding `brace-expansion` and `devalue`
  advisories. The console is named Studio, and installation and editor-schema
  URLs use `skali.dev`.

## Upgrade notes

Use the matching CLI release and upgrade the daemon before renaming environments.
Existing manifests, revisions and backup objects are retained. Old backup
directories stay in place; snapshots without a live owner remain discoverable
to project admins and require an explicit restore target.

Replace installation-wide public S3 URLs with per-bucket routes. The legacy S3
edge cleanup, open-policy cleanup and installation-record reader remain included
so rc.10 installations can upgrade directly. They must also remain in the first
stable release. Cross-environment private ingress that previously worked is
blocked after reconciliation; declare public routes where communication is
needed. See [upgrade safety](prerelease-safety.md) and [limitations](limitations.md).

## Publication and stable-release gate

After this branch is reviewed and merged, run `task release:check`, then
`task release:tag V=v0.1.0-rc.11` from a clean, synchronized `main`. Pushing the
tag publishes release binaries and images through the release workflow.

Before the stable tag, complete the released-binary rehearsal recorded in
[ROADMAP.md](../ROADMAP.md): clean Linux and Mac installs, development outside
the repository, upgrades from the preceding release, joining a node without a
local hostd, password reset, and a Studio software update. Local tests and the
nonpublishing snapshot rehearsal do not substitute for those checks.

The large-bucket performance target in #98 still needs measurement against the
actual remote backup target; local correctness checks do not establish remote
throughput. Larger deferred work (#31, #90 and #93) remains outside this release.
