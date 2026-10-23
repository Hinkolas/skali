# v0.1.3 release roadmap

Local working checklist for moving from the current release candidates to
`v0.1.3` on the stable channel. **Keep this file uncommitted unless explicitly
requested otherwise.** Update it as implementation commits and verification
results accumulate.

## Recommendation and scope

Freeze feature scope, fix the selected correctness issues, publish another RC,
and rehearse that candidate before cutting stable. There is no need to close
the entire backlog for this release.

The proposed scope is **12 existing issues**: seven correctness fixes, three
bounded deployment fixes, and two verification tasks. Larger features and
production-readiness commitments remain separate.

Later scope decisions add #249. #242 was added on 2026-10-22 and moved out
again on 2026-10-23: the S3-backed registry follows #257 in v0.1.4, and the
registry dependency finding D2 from #184 is fixed in v0.1.3 as a plain image
update on the existing filesystem storage, verified in rc.9.

“Stable” here means the stable distribution channel. The project's documented
0.x initial-development limitations still apply.

## Assessment baseline

| Item | Assessed state |
| --- | --- |
| Assessment date | 2026-10-22 |
| Reviewed commit | `d25a97cdfa08052cb42e45a2b9d17309583e17ca` |
| Stable release | [`v0.1.2`](https://github.com/Hinkolas/skali/releases/tag/v0.1.2), published 2026-10-10 |
| Latest candidate | [`v0.1.3-rc.7`](https://github.com/Hinkolas/skali/releases/tag/v0.1.3-rc.7), published 2026-10-21 |
| Main beyond RC7 | One commit: SQL sessions and exec automation improvements (#239) |
| Repository state | Clean `main`, matching GitHub main at assessment time |
| Backlog | 52 open issues; no open PRs; none of the open issues had a milestone |
| Changes since v0.1.2 | 342 files; approximately 29,000 added lines; five new migrations, 00014–00018 |

This is a dated assessment, not a live issue-status mirror. Recheck the branch,
release tags, issue status, and advisory results before acting on it. The code
mechanisms behind the principal findings were inspected; the cluster incidents
were not reproduced live during the assessment.

### What the candidates contain

| Candidate | Main changes |
| --- | --- |
| RC1 | Backup cancellation and cleanup; delay release of dropped stateful services until activation; responsive CLI work. |
| RC2 | Snapshot verification and incomplete-restore holds; project deletion; route aliases; read retries; manifest documentation and example validation. |
| RC3 | Deployment history, per-service progress, change summaries, and cancellation during builds/uploads. |
| RC4–6 | Remote dev proxy, macOS privileged-port/socket fixes, upload progress, and installer next steps. |
| RC7 | In-place dev upgrades; partial-cluster reset; multi-extension readiness and route activation fixes; Go/Vite updates. |
| Current main | SQL sessions and automation improvements to exec, not yet published in an RC. |

The cycle contains substantial safety improvements, but later candidates also
continued to add functionality. The next phase should stabilize the selected
scope and validate real upgrades.

## How to track progress

- Update implementation status and commit references in the table below after
  each change. Use `pending`, `in progress`, `implemented`, or `verified`.
- Mark an issue's acceptance checklist complete only when its required checks
  have actually passed. A merged change alone is not verification.
- Record commands, environments, exact commits/releases, failures, and skips in
  the verification log. Do not count skipped cases as passing.
- Record scope changes and accepted limitations in the decision log. A newly
  reproduced defect in a supported configuration can move into this release.
- Keep substantive changes after the candidate rehearsal behind another RC and
  relevant retesting.

Suggested GitHub organization, not yet created: a `v0.1.3` milestone for the
selected issues and one release-rehearsal issue collecting candidate evidence.

## Selected implementation scope

| Issue | Deliverable | Status | Implementation commit(s) | Verification evidence |
| --- | --- | --- | --- | --- |
| [#152](https://github.com/Hinkolas/skali/issues/152) | Exclusive daemon execution, bounded shutdown, completion recovery | implemented; full platform upgrade check open | `e6fca0b` ([PR #240](https://github.com/Hinkolas/skali/pull/240)) | DB tests, live strategy-switch test, local SIGTERM run (see log) |
| [#148](https://github.com/Hinkolas/skali/issues/148) | Promotion-bound release Jobs and safe fallback | implemented | `739b83a` ([PR #241](https://github.com/Hinkolas/skali/pull/241)) | DB tests; four regression tests fail on the old code. No live run. |
| [#141](https://github.com/Hinkolas/skali/issues/141) | Bind run adoption/completion to the rollout | implemented | `9678e75` ([PR #244](https://github.com/Hinkolas/skali/pull/244)) | DB tests; new deterministic tests fail on the old code. No live run. |
| [#150](https://github.com/Hinkolas/skali/issues/150) | Run-independent rollout deadline and safe traffic-switch boundary | implemented | `9678e75` ([PR #244](https://github.com/Hinkolas/skali/pull/244)) | As #141 |
| [#151](https://github.com/Hinkolas/skali/issues/151) | Close owned runs on every failure path | implemented | `9678e75` ([PR #244](https://github.com/Hinkolas/skali/pull/244)) | As #141 |
| [#143](https://github.com/Hinkolas/skali/issues/143) | Detect database connection exhaustion and propagate dependency health | implemented | `f9c2523` ([PR #245](https://github.com/Hinkolas/skali/pull/245)) | Unit/DB tests; live `TestLivePoolConnectionExhaustion` on k3d (53300 with ready instances, incident open/close); live check that the new pool spec reloads without restart |
| [#167](https://github.com/Hinkolas/skali/issues/167) | Bound stalled backup/restore transfers | implemented | `34af848` ([PR #246](https://github.com/Hinkolas/skali/pull/246)) | DB tests incl. `-race`; live backup suite on k3d (bucket restore through SeaweedFS with the progress hook). A stalled remote S3 target was not reproduced live. |
| [#145](https://github.com/Hinkolas/skali/issues/145) | Managed registry upload-timeout default | implemented | `5239758` ([PR #247](https://github.com/Hinkolas/skali/pull/247)) | Bundle tests; on k3d a 100 KB/s blob PATCH failed with 502 at 61s before and returned 202 after 87s with the overlay. Verified on khz with rc.8 (2026-10-23): authenticated 100 MiB push held one registry connection for 210s and succeeded; interrupted and dropped uploads left the active revision untouched. |
| [#194](https://github.com/Hinkolas/skali/issues/194) | Reject unsupported stateful transitions before deployment | implemented | `7a7d7d9` ([PR #248](https://github.com/Hinkolas/skali/pull/248)) | DB tests (preview, open with destructive allowed, live-state drift, promotion recheck, rollback); all fail with the check disabled. No live deploy run. |
| [#195](https://github.com/Hinkolas/skali/issues/195) | Reject bucket quota reductions below usable capacity | implemented | `7a7d7d9` ([PR #248](https://github.com/Hinkolas/skali/pull/248)) | DB tests (below/equal/above usage, unmeasurable, usage growing before promotion); live quota test on k3d confirms the measurement equals what upkeep enforces. |
| [#249](https://github.com/Hinkolas/skali/issues/249) | Destructive confirmation and volume-shrink check on rollback (added to scope) | implemented | `2912a2b` ([PR #250](https://github.com/Hinkolas/skali/pull/250)) | DB, API, CLI and client tests; the refusal tests fail with the gate disabled. Studio dialog not exercised in a browser. |
| [#184](https://github.com/Hinkolas/skali/issues/184) | Current dependency and bundled-runtime risk review | reviewed; release remediation open | `56dccc9` (PR #251: Vitest, review, audit tooling) | [Dated review](dependency-review-v0.1.3.md): Vitest patched; 12 source, 86 image and 8 native-tool scans; follow-ups D1–D8 remain |
| [#204](https://github.com/Hinkolas/skali/issues/204) | Live route-alias and Studio verification | done | `c7dc0e3` ([PR #254](https://github.com/Hinkolas/skali/pull/254)) | rc.7 on skali dev and khz: redirects, 301/308, TLS policies, Let's Encrypt per alias, deferral, backend down, Studio. Defect: an alias's own `${VAR}` is redacted in its checkpoint, breaks `route probe`, shows as placeholder without a certificate. Non-default edge port not exercised. |

### 1. Upgrade safety — #152

The skalid Deployment has one replica but no explicit rollout strategy. Boot
recovery can act while the old daemon still runs, and server-owned preparing
deployments are failed on boot rather than resumed.

Evidence: [bundle rendering](../internal/bundle/bundle.go),
[deployment boot recovery](../internal/deploy/open.go), and
[daemon startup/shutdown](../cmd/skalid/main.go).

- [x] Prevent simultaneous unfenced daemons, using the bounded `Recreate`
  mitigation or an equivalently verified exclusive-execution mechanism.
  Existing installs switch via a merge patch, because server-side apply
  keeps the defaulted `rollingUpdate` and the API server rejects it.
- [x] Stop accepting work on shutdown and reach a bounded safe point before
  cancelling the workers (20s drain, 5s request grace, 5s loop stop, 45s
  termination grace).
- [x] Recover persisted completion work interrupted between artifact
  verification and promotion (resumed once; a second interruption or
  unverified artifacts fail it).
- [ ] Verify upgrades with in-flight work, failure, and restart recovery.
  DB-level interrupt/resume and the live strategy switch are covered; a full
  `skali dev upgrade` or cluster upgrade from rc.7 is still open.
- [x] Document the brief API interruption (`docs/limitations.md`). Keep
  zero-downtime ownership and handover in #115 outside this fix.

### 2. Migration correctness — #148

Failed release Jobs are classified using
`status.Created.Before(target.UpdatedAt)`, comparing timestamps with different
precision. Pruning does not protect a running release Job when fallback removes
it from the desired set.

Evidence: [release handling](../internal/reconcile/release.go) and
[pruning](../internal/reconcile/plan.go).

- [x] Bind Jobs to their promotion, including genuinely new forced redeploys.
  `environment_targets.promoted_at` (migration 00019) + `skali.dev/promotion`
  Job label; unlabeled legacy Jobs compare at whole-second precision.
- [x] A command that fails within the promotion second remains failed and is
  not executed again for that promotion.
- [x] Fallback preserves a running migration until completion or its own timeout.
- [x] New promotions execute their intended release; terminal Jobs can be
  reclaimed safely.
- [x] Keep run outcomes and migration diagnostics accurate.
- [x] Add deterministic regression coverage for same-second failure and
  fallback during a running migration. Four new tests fail on the old code.

Keep disruption handling (#165) and latency optimization (#166) separate.

### 3. Rollout ownership and outcomes — #141, #150, #151

Treat these as one coordinated workstream with independently reviewable changes.
Adoption does not enforce a rollout identity; the deadline depends on an adopted
run; failed `Open` cleanup still uses the opening request's context.

Evidence: [run adoption](../internal/reconcile/runattach.go),
[activation and deadlines](../internal/reconcile/reconcile.go), and
[opening cleanup](../internal/deploy/open.go).

**#141 narrowed (done):** the issue body now scopes the handoff marker, bound
adoption, the success mislabel and the tests; cancelling a run before its
handoff moved to [#243](https://github.com/Hinkolas/skali/issues/243).
Original note: restart already takes the environment
lock in [deploy.go](../internal/deploy/deploy.go). The new
[run-deployment metadata](../internal/deploy/rundeployment.go) is explicitly
explanatory and does not enforce adoption. Do not redo the locking fix or treat
the metadata addition as completion of the issue.

- [x] #141: adopt and complete only the run corresponding to the rollout being
  activated; test deploy, forced redeploy, rollback, and restart races.
- [x] #141: prevent successful run completion from mislabelling an owner's
  still-running work as cancelled.
- [x] #150: enforce rollout deadlines without depending on an attached run.
- [x] #150: account for release timing correctly and prevent fallback in the
  pass that just switched traffic without a confirmation opportunity.
- [x] #151: use bounded cleanup detached from failed/disconnected requests.
- [x] #151: finish every run a pass owns on early failures without finishing
  another owner's adopted run.
- [x] #151: identify the actual blocking run and provide an actionable hint.
- [x] Verify that these scenarios leave no leaked environment operation slot.
  Live on khz with rc.8 (2026-10-23): Ctrl-C during build and upload, failed
  release command, detached rollout past its deadline, forced redeploy while
  running (refused, naming the run), Ctrl-C attached during the release
  command, `run cancel` during it, and rollback all left no running run. A
  client losing the network mid-upload leaves the deployment running until
  the stale sweep fails it (31 min); the CLI then wrongly reports the
  environment unchanged. Declared rollout timeouts under 10m are ignored
  (`rolloutBudget` floors at the kernel default).

### 4. Truthful database health — #143

Provisioned databases become healthy based on ready pool instances. Missing
exporter metric families are summed as zero. Neither establishes that a new
ordinary application connection can succeed during connection starvation.

Evidence: [database evaluation](../internal/module/database/database.go) and
[pool metrics](../internal/metrics/pools.go).

- [x] Exhaust ordinary-user connection slots while pods remain ready and an
  existing connection remains usable (live `TestLivePoolConnectionExhaustion`).
- [x] Detect the failure within a documented bound through a low-impact path
  that remains usable during starvation: a `pg_monitor` probe role over the
  primary's local socket, two confirmations at 15s (`docs/databases.md`).
- [ ] Propagate degradation and an actionable cause through dependent databases,
  environments, projects, API, CLI, and Studio, preserving access boundaries.
  Done for health, API (`connections`, `incidents`), `skali database` and the
  Studio pool pages. Gap: the remote CLI has no command that shows
  environment health to project members.
- [x] Treat missing metrics, exporter failures, failed scrapes, and stale
  observations as unknown/unavailable, not zero or recovered.
- [x] Recover only after fresh application-relevant successful checks, without
  an unnecessary redeploy.
- [x] Expose incident identity/state/timing for later notification work
  (`pool_connection_incidents`, migration 00022).

Do not make detection wait for a dashboard redesign, connection-budget policy
(#164), or webhook delivery (#116).

### 5. Bounded backup and restore — #167

Bucket copies have retries but no component/stall deadline. The controller
defaults to one worker, so a stalled transfer can indefinitely hold up other
backup and restore work.

Evidence: [copy handling](../internal/backup/copy.go) and
[controller defaults](../internal/backup/controller.go).

- [x] Bound component execution and sustained lack of transfer progress: two
  minutes without data moving per object attempt, `SKALI_BACKUP_COPY_TIMEOUT`
  (6h) per bucket component and for a restore's snapshot check.
- [x] Retry transient observation failures with bounded, cancellation-aware
  backoff (Job status, workload stop, reconverge; up to two minutes).
- [x] A stalled transfer ends with a useful failure and releases worker/run
  resources.
- [x] Cancellation interrupts retries and copies promptly.
- [x] Failure or timeout preserves the incomplete-restore hold; never blindly
  replay destructive restore work.
- [x] Define the bounded scheduled-backup retry behavior within its window:
  retries after 5 and 30 minutes, only while the next fire is at least that
  far away; cancelled backups and restores are never retried (migration 00023).

### 6. Bounded deployment fixes — #145, #194, #195

These address normal user paths and are recommended for the same cycle.

- [x] #145: render a deliberate managed registry upload-timeout default and
  document its scope if it affects the shared HTTPS entrypoint: 30 minutes
  on `websecure` (Traefik has no per-route read timeout), documented in
  `docs/limitations.md`.
- [x] #145: verify an authenticated upload lasting more than 60 seconds,
  ordinary authentication/API/application traffic, and persistence through
  reconciliation/upgrade. Failed uploads must leave the deployment untouched.
  Done on k3d with an unauthenticated registry behind the same entrypoint;
  the authenticated push and upgrade persistence remain for khz/candidate
  verification. Done on khz with rc.8 after the rc.7 → rc.8 upgrade.
- [x] Keep the full configurable settings surface in #120 separate. The upload
  progress changes and manual cluster workaround did not implement this fix.
- [x] #194: reject unsupported database major, engine, or isolation transitions
  before builds/promotion, with the same classification in plan and API paths.
  Plan preview, open, promotion and rollback share one check; the API
  answers `422 unsupported_transition`. The preview refuses rather than
  listing a classified row.
- [x] #194: compare with actual live claims, recheck near application where
  needed, and preserve targets, credentials, and data on rejection.
- [x] #194: destructive confirmation cannot bypass unsupported transitions;
  initial supported creation and allowed in-place updates still work.
- [x] Keep the data-preserving migration feature in #196 out of scope.
- [x] #195: validate quota reductions against fresh authoritative usage, define
  the equality boundary, and reject missing/stale usage rather than guessing.
  Usage at or above the new quota is refused; unmeasurable usage answers
  `503 usage_unavailable`.
- [x] #195: recheck near application and document residual concurrency/overshoot
  semantics. Rejection preserves the previous quota and deployment target.
  Promotion measures again; the window until upkeep applies the quota is
  documented in `docs/buckets.md`.

Relevant code: [Traefik bundle](../internal/bundle/bundle.go),
[deployment admission](../internal/deploy/open.go),
[database claims](../internal/dbstore/claims.go), and
[bucket claims](../internal/dbstore/bucketclaims.go).

### 7. Verification tasks — #184 and #204

- [x] #184: update the affected Vitest development tooling and rerun current
  Go/npm audits against exact release inputs.
- [x] #184: review relevant Go, Studio, build-tool, and bundled-runtime/container
  dependencies. Record dates, versions, fixes, accepted exceptions, and
  follow-ups. Source scans alone do not assess every shipped image.
- [x] #184: keep dependency changes small and independently reviewable.
- [ ] Resolve or explicitly disposition dependency follow-ups D1–D8 in the
  [dated review](dependency-review-v0.1.3.md) before stable: PostgreSQL images
  and existing-pool patch upgrades, registry 2.8.3, k3s/edge, SeaweedFS and
  operators, optional Longhorn, build tools, installer tools and runtime base.
  A completed inventory is not release-security sign-off.
- [ ] PostgreSQL D1: pins 17.11/18.6 with automatic, serialized pool patch
  updates in `ac21753` (PR #252; unit, DB and k3d live
  tests and CI pass). Remaining: rc.7 upgrade rehearsal with existing pools, and
  the tenant follow-ups from [the review record](postgres-patch-updates.md#record)
  in the release notes.
- [ ] Registry D2: replace registry 2.8.3 with a maintained image on the
  existing filesystem storage (no S3, no replicas; those stay in #242 for
  v0.1.4). Verify token auth, push/pull, import/blob mounting, existing data
  after the upgrade and the slow authenticated upload in rc.9.
- [x] #204: verify alias redirects on live Traefik, preserving paths and queries.
- [x] #204: verify GET/non-GET behavior, including a form POST's method.
- [x] #204: exercise automatic, optional, and disabled TLS, certificate issuance,
  independent DNS deferral, and ACME handling.
- [x] #204: redirects work with an unavailable application backend.
- [x] #204: visually verify Studio alias rows, links, and deferred states/counts.
- [x] #204: results recorded on the issue.
- [x] #204: PR #254 merged as `c7dc0e3`; #204 closed.
- [ ] Record exact candidate, environment, cases, and observed results.

## Release sequence

### Phase A — Freeze and implement

- [ ] Confirm the proposed 12-issue scope and record any changes below.
- [x] Implement #152 and #148 first.
- [x] Complete #141/#150/#151 as the coordinated rollout workstream.
- [x] Complete #143, #167, and the three bounded deployment fixes. All
  merged; #145's authenticated push on khz is left for candidate
  verification.
- [ ] Keep other new feature families outside the freeze. #242 and #257 are
  scheduled for v0.1.4 (#257 first).

### Phase B — Prepare the next candidate

- [x] Recheck the latest tag; use `v0.1.3-rc.8` only if it is still next.
- [x] Run full CI-equivalent checks, current dependency review, and the
  nonpublishing snapshot rehearsal on the selected commit. PR #255 CI green;
  `scripts/check-release-snapshot.sh` passed locally with GoReleaser 2.18.3;
  D6/D8 dispositioned in the dependency review.
- [x] Write candidate notes identifying the selected fixes and remaining limits.
  Record the registry D2/#242 deferral for this RC explicitly.
- [x] Record the candidate commit and artifact identities below.
- [x] Publish the candidate only after explicit authorization to push the tag.
  Authorized 2026-10-22 ("then cut rc.8").

Candidate tag: **`v0.1.3-rc.8`**  
Candidate commit: **`b398900`**  
Release workflow: **[run 39841845436](https://github.com/Hinkolas/skali/actions/runs/39841845436)**, all jobs passed  
Release/artifact links: **[v0.1.3-rc.8](https://github.com/Hinkolas/skali/releases/tag/v0.1.3-rc.8)** (prerelease, 9 assets; stable channel still v0.1.2); image `ghcr.io/hinkolas/skalid:v0.1.3-rc.8` `sha256:03e6bb57dff4115476adab6c6a9fb9859f2f2352132b2ba469db1a6fe1c62da4` (amd64, arm64)  
Resolved build tools: GoReleaser v2.18.3, BuildKit v0.34.1, Node 22.23.3, Go from go.mod; actions checkout@v5 `fbc6f399`, setup-go@v6 `924ae3a1`, setup-node@v5 `a0853c24`, setup-qemu@v4 `99012661`, setup-buildx@v4 `f87e5991`, login@v4 `dbcb8138`, goreleaser-action@v7 `a4f02ab5`

Candidate tag: **`v0.1.3-rc.9`** (authorized 2026-10-23, "yes pls")  
Candidate commit: **`2ddaf7b`**  
Release workflow: **[run 39997590976](https://github.com/Hinkolas/skali/actions/runs/39997590976)**, all jobs passed  
Release/artifact links: **[v0.1.3-rc.9](https://github.com/Hinkolas/skali/releases/tag/v0.1.3-rc.9)** (prerelease, 9 assets; stable channel still v0.1.2); image `ghcr.io/hinkolas/skalid:v0.1.3-rc.9` `sha256:fde28f99d9c504df53944424d09fd3aac1c4475e41516c422f2cead902ede09b` (amd64, arm64); `release.json` names k3s `v1.36.5+k3s1`  
Pre-tag checks: `scripts/check-release-snapshot.sh` passed; govulncheck 1.7.0 unchanged from rc.8 (0 called); no Go, Dockerfile, GoReleaser or workflow changes since rc.8; PostgreSQL 18.6 image rescan: libxml2 still without a Debian fix

### Phase C — Rehearse the released candidate

- [ ] Complete the acceptance checklist below using actual released binaries.
- [ ] Observe at least one scheduled-backup cycle and an upgrade/restart cycle.
- [ ] Allow 24–48 hours of representative use as a useful minimum observation
  period; elapsed time does not substitute for the required scenarios.
- [ ] Record defects and rerun affected checks after fixes.
- [ ] Publish another RC for substantive changes after the rehearsal.
- [x] rc.9: #256 plus D4 (SeaweedFS, CNPG, cert-manager), D3 (k3s/edge),
  D7 (k3d/Lima) and D2 (registry image update), one PR each in that order;
  published as `v0.1.3-rc.9` on 2026-10-23. Still to do: rehearse rc.8 →
  rc.9 on khz, including registry data surviving the update and the slow
  authenticated push.

### Phase D — Cut stable

- [ ] Every selected issue is verified, or an explicit scope decision and its
  residual risk are recorded below.
- [ ] No unresolved release-blocking failures remain in the rehearsal.
- [ ] Publish notes covering the entire `v0.1.2` → `v0.1.3` change, not only the
  difference from the immediately preceding RC.
- [ ] Include migrations, upgrade/recovery instructions, and known limitations.
- [ ] Verify a clean, synchronized release commit and obtain explicit tag-push
  authorization. `task release:tag` creates and pushes the tag; it is not a
  read-only check.
- [ ] After publication, verify stable/latest selection, release metadata,
  checksums, image architectures, and website release/schema selection.

Stable tag: **TBD**  
Stable commit: **TBD**  
Release URL: **TBD**  
Final verification record: **TBD**

## Release acceptance checklist

### Automated checks and packaging

- [ ] DB-backed Go tests, vet, formatting, and generated-file/schema checks.
- [ ] Studio checks, tests, lint, and build; website checks, lint, and build.
- [ ] Current dependency audits, including example Go modules and the separate
  bundled-runtime review.
- [ ] `task release:check` and `scripts/check-release-snapshot.sh`.
- [ ] Appropriate live suites from [testing.md](testing.md), including relevant
  reconciliation, backup, substrate, dev, and installer coverage.
- [ ] Clean Linux and macOS installs; correct CLI/daemon version dispatch.
- [ ] Published checksums and `release.json` agree with the candidate; both image
  architectures are present and usable.
- [ ] RC publication leaves stable/latest unchanged; stable publication moves
  the stable channel as intended.

`task release:check` only validates GoReleaser configuration. The existing
[CI workflow](../.github/workflows/ci.yml) runs DB-backed tests and packaging
checks, but does not run live cluster/dev/installer suites or the separate
[advisory audit](../.github/workflows/audit.yml). A green release workflow is
necessary but insufficient for this checklist.

### Actual released-version upgrades

- [ ] Populate a released `v0.1.2` installation and upgrade it to the candidate.
- [ ] Upgrade a populated RC7 installation to the candidate.
- [ ] Exercise both CLI and Studio upgrade paths.
- [ ] Verify rows, bucket objects, volume files, encrypted values, accounts,
  routes, and run history through migrations 00014–00023 (00019–00023 are new
  in this cycle: promotion time, rollout start, pool incidents, schedule
  attempts).
- [ ] Exercise interrupted upgrade, restart recovery, and in-flight work.

The existing [dev-upgrade E2E test](../cmd/skali/dev_upgrade_e2e_test.go) builds
both its “old” and “new” versions from current source. It exercises upgrade
mechanics but does not establish historical-schema compatibility with an actual
released `v0.1.2` installation.

### Local development and new SQL/exec behavior

- [ ] Actual released-binary `skali dev upgrade`, interruption/resume, retained
  databases/buckets/volumes, and unchanged development CA.
- [ ] Partial local-cluster reset recovery and macOS remote-proxy behavior.
- [ ] SQL scripts and typed JSON against a real database/client Job.
- [ ] SQL/exec permission boundaries and protected-environment checks.
- [ ] Slow client startup, large stdin, timeout/SIGINT, terminal restoration,
  exit codes, and cleanup of temporary client Jobs.

### Deployment, edge, and recovery scenarios

- [ ] Same-second migration failure and fallback during a running migration.
- [ ] Forced redeploy/restart, run-less timeout, failed opening request after
  client disconnect, and rollout completion at the traffic-switch boundary.
- [ ] Ordinary-user database connection exhaustion, unavailable/stale metrics,
  dependency-health propagation, and recovery.
- [ ] Corrupt snapshot refusal before destructive work.
- [ ] Cancel/restart during restore; hold enforcement; cleanup before retry or
  accept; restore of data written before the platform upgrade.
- [ ] Stalled remote S3 transfer and queued backups.
- [x] Long authenticated registry upload and route-alias checks from #204.
- [ ] A short, tested release-specific upgrade/recovery document links the
  authoritative existing instructions and states unsupported recovery paths.

Use disposable fixtures for destructive checks. Record skipped or unavailable
platforms explicitly rather than treating them as covered.

## Deferred backlog: all remaining 40 open issues

These are release recommendations, not changes to GitHub labels. Combined with
the original 12 selected issues, the tables cover all 52 issues open at
assessment time. #243 below and #249 above were also opened after the
assessment. #242 was in the selected scope from 2026-10-22 to 2026-10-23 and
is now scheduled for v0.1.4 after #257.

### Next reliability and operational follow-ups

| Issue | Recommendation |
| --- | --- |
| [#155](https://github.com/Hinkolas/skali/issues/155) | Fix queued-backup slot reservation and recover never-started pending backups. Cancellation improvements already shipped do not complete this. |
| [#175](https://github.com/Hinkolas/skali/issues/175) | Bound multipart memory and define live-file consistency. Prioritize if large volume backups are common. |
| [#158](https://github.com/Hinkolas/skali/issues/158) | Make credential retirement consumer-aware and safe across overlapping rotations, before scheduled rotation. |
| [#157](https://github.com/Hinkolas/skali/issues/157) | Reproduce multi-node volume attachment behavior before choosing affinity; establish measured resource requests. The reported placement problem is not yet proven. |
| [#164](https://github.com/Hinkolas/skali/issues/164) | Rescoped to PgBouncer in front of every pool with declared session/transaction budgets; adds PgBouncer signals to #143's pool health record. Feature work, after v0.1.3. |
| [#171](https://github.com/Hinkolas/skali/issues/171) | Reproduce slow managed-database teardown before fixing the suspected ordering cause. |
| [#172](https://github.com/Hinkolas/skali/issues/172) | Add attachment reconnect/resume after safe daemon upgrades. Generic GET retries do not finish this work. |
| [#177](https://github.com/Hinkolas/skali/issues/177) | Correct existing-cluster bootstrap prompts without expanding into the full settings workflow. |
| [#118](https://github.com/Hinkolas/skali/issues/118) | Build the full recovery runbook; release-specific rehearsals happen now, full platform-state recovery depends on #111. |
| [#126](https://github.com/Hinkolas/skali/issues/126) | Follow the short release instructions with a coherent operator guide linking existing documentation. |
| [#129](https://github.com/Hinkolas/skali/issues/129) | Implement registry retention/GC with protected-reference semantics; monitor disk growth meanwhile. |
| [#121](https://github.com/Hinkolas/skali/issues/121) | Define authenticated checksum verification separately from a small dependency patch. |
| [#180](https://github.com/Hinkolas/skali/issues/180) | Publish SBOMs from actual release inputs; inventory does not replace risk review. |
| [#181](https://github.com/Hinkolas/skali/issues/181) | Define and verify independent reproducible rebuilds as a separate hardening task. |

Bring a follow-up into the current scope if the release rehearsal reproduces a
blocking defect on a supported configuration; record that decision explicitly.

### Production-readiness scope

| Issue | Boundary for this release |
| --- | --- |
| [#111](https://github.com/Hinkolas/skali/issues/111) | Platform-state backup/fresh-install recovery remains a prerequisite for stronger disaster-recovery claims. Environment backups do not cover all platform state. |
| [#112](https://github.com/Hinkolas/skali/issues/112) | PostgreSQL PITR is a separate recovery feature. Do not imply logical snapshots provide it. |
| [#114](https://github.com/Hinkolas/skali/issues/114) | Object-store metadata still requests single-instance availability. This becomes a blocker if this release promises multi-node storage availability. |

Do not present `v0.1.3` as solving these gaps. See the
[metadata claim](../internal/substrate/objectstore.go),
[known limitations](limitations.md), and [security/recovery notes](security.md).

### Keep outside the stabilization scope

| Issue | Reason/dependency |
| --- | --- |
| [#90](https://github.com/Hinkolas/skali/issues/90) | External database routing is a separate feature. SQL sessions on main already cover some interactive access needs without public endpoints. |
| [#113](https://github.com/Hinkolas/skali/issues/113) | Explicit retained-PVC reclamation needs a separate destructive-operation contract; current retention is intentional. |
| [#115](https://github.com/Hinkolas/skali/issues/115) | Zero-downtime upgrades follow #152's safe brief interruption. |
| [#116](https://github.com/Hinkolas/skali/issues/116) | Webhook delivery follows #143 incident detection; remains needed for stronger unattended-operation claims. |
| [#120](https://github.com/Hinkolas/skali/issues/120) | General persisted cluster settings must not delay #145's managed timeout default. |
| [#123](https://github.com/Hinkolas/skali/issues/123) | Registry mobility is a separate feature; the removal guard is an explicit limitation. |
| [#127](https://github.com/Hinkolas/skali/issues/127) | Faster first dev startup does not justify new image infrastructure during stabilization. |
| [#128](https://github.com/Hinkolas/skali/issues/128) | Scheduled rotation remains deferred and depends on #158. |
| [#159](https://github.com/Hinkolas/skali/issues/159) | Partial-snapshot GC needs its own race-safe destructive cleanup design. |
| [#162](https://github.com/Hinkolas/skali/issues/162) | Authorization query optimization is not a release gate. |
| [#165](https://github.com/Hinkolas/skali/issues/165) | Release-pod disruption handling follows migration correctness; preserve final command failures. |
| [#166](https://github.com/Hinkolas/skali/issues/166) | Small release-handoff latency improvements stay behind #148. |
| [#169](https://github.com/Hinkolas/skali/issues/169) | Background-reconcile supersession introduces concurrency policy; first make ownership correct. |
| [#170](https://github.com/Hinkolas/skali/issues/170) | Large-bucket deletion optimization follows measured evidence and safe cleanup. |
| [#173](https://github.com/Hinkolas/skali/issues/173) | Queued/executing presentation follows correct ownership and outcomes. |
| [#174](https://github.com/Hinkolas/skali/issues/174) | Backup concurrency follows #155 queue admission/recovery. |
| [#176](https://github.com/Hinkolas/skali/issues/176) | Detailed backup progress follows bounded transfers and reliable cancellation. |
| [#183](https://github.com/Hinkolas/skali/issues/183) | Password-policy changes are deliberately deferred; not incidental release cleanup. |
| [#192](https://github.com/Hinkolas/skali/issues/192) | Per-object bucket checksums strengthen snapshots; retain and document the current key/size verification limitation. |
| [#196](https://github.com/Hinkolas/skali/issues/196) | Data-preserving database migration is a new workflow; #194 provides the bounded immediate fix. |
| [#197](https://github.com/Hinkolas/skali/issues/197) | Unused-pool teardown is a new destructive administration workflow. |
| [#198](https://github.com/Hinkolas/skali/issues/198) | Allowed-major policy is separate from safe deployment admission. |
| [#220](https://github.com/Hinkolas/skali/issues/220) | Compose export is new portability functionality. |
| [#243](https://github.com/Hinkolas/skali/issues/243) | Cancelling a run before its handoff, split from #141 (opened after the assessment); low priority. |

## Verification already performed during assessment

These observations establish the baseline only. They do not mark the future
candidate's acceptance checklist complete.

| Check | Result and limits |
| --- | --- |
| RC7 publication | [Release workflow passed](https://github.com/Hinkolas/skali/actions/runs/39609252557). |
| Latest feature PR | [Checks passed](https://github.com/Hinkolas/skali/actions/runs/39770810195): Go, secrets, release-config; Studio/website jobs were skipped for this Go-only change. |
| Local Studio tests | 79/79 passed across 17 files. |
| Local ordinary Go tests | The only failing package was the CLI; that entire package passed after correcting the terminal environment. DB-backed, live cluster, Docker, and dev/installer E2E checks were disabled/skipped. |
| CLI terminal finding | The elapsed-time assertion in `TestAttachViewStaysLiveWhileTheServerStalls` failed three isolated runs with `NO_COLOR=1`/`TERM=dumb`. Rendering suppresses that text in this mode. Full CLI package passed with `NO_COLOR` unset and `TERM=xterm-256color`. Pin the test's terminal contract or test both modes; this is not evidence of a cluster defect. |
| Go source advisories | No reachable vulnerabilities found in main code or any of the four example modules on darwin/arm64 using Go 1.26.10. Uncalled module advisories remain for review. |
| Example toolchain caveat | Initial scans selected the machine's Go 1.26.5 and reported stdlib findings. Rerunning with the release's Go 1.26.10 removed reachable findings. Match scans to release inputs. |
| Website npm audit | Zero findings. |
| Studio npm audit | Two moderate package findings involving Vitest/@vitest/mocker; fixes available. See [mock registration](https://github.com/advisories/GHSA-qxg6-89mg-rqv8) and [Windows traversal](https://github.com/advisories/GHSA-xp26-3454-mjgp). |
| Bundled runtime/container risks | Not comprehensively assessed; remains in #184. |

The latest recorded GitHub audit predates RC7's dependency patch; its old red
result is not proof the same findings remain. Fresh dated scans are required.
The historical v0.1.0 rehearsal issue #110 is already closed and is not an open
gate for this release; record a new rehearsal against this candidate.

## Verification log

Add one row per meaningful run, including failures and skipped cases. Link
durable logs or attach them to the release-rehearsal issue when available.
The `7f806a2` and `14b7d0b` rows are branch commits; they landed on main
squashed as `e6fca0b` and `739b83a`.

| Date | Commit/release | Environment | Command or scenario | Result, skips, evidence |
| --- | --- | --- | --- | --- |
| 2026-10-22 | `d25a97c` | Local darwin/arm64 | Baseline source review, Go/Studio tests, Go/npm audits | See assessment results above; live release rehearsal not performed. |
| 2026-10-22 | `7f806a2` (#152) | Local darwin/arm64, dev Postgres | `go vet` + `go test` over all Go packages with `TEST_DATABASE_URL` | Passed. Includes interrupt-then-resume, drain, resume-once, and unverified-artifact recovery tests. Live tests other than the one below were not run. |
| 2026-10-22 | `7f806a2` (#152) | k3d `skali-test` | `TEST_KUBECONFIG=… go test ./internal/bundle -run TestLive` | Passed: an existing RollingUpdate Deployment moves to Recreate with no new ReplicaSet. Plain SSA was first confirmed to be rejected on this cluster. |
| 2026-10-22 | `7f806a2` (#152) | Local skalid, API-only | SIGTERM with an open project event stream | Exit 0 after 5.35s (request grace), "shutdown complete" logged. Previously a held stream hit the 10s deadline and exited 1. |
| 2026-10-22 | `14b7d0b` (#148) | Local darwin/arm64, dev Postgres | `go vet` + `go test` over all Go packages with `TEST_DATABASE_URL` | Passed. New same-second failure (labeled and legacy), forced-redeploy rerun, cancel-during-migration, and prune tests; all four fail against the old classification and pruning. No live cluster run. |
| 2026-10-22 | PR #244 → `9678e75` (#141, #150, #151) | Local darwin/arm64, dev Postgres; GitHub CI | `go vet` + `go test` over all Go packages | Passed; new deterministic tests fail against the old code. No live cluster run. |
| 2026-10-22 | PR #245 → `f9c2523` (#143) | Local darwin/arm64, dev Postgres; k3d `skali-test`; GitHub CI | `go vet` + `go test`; Studio check/vitest/lint; `TestLivePoolConnectionExhaustion`, `TestLivePoolApplySendsNothingUnchanged` | Passed. Live: logins refused with 53300 while instances stay ready, refusal confirmed on the second check, incident opened and closed after release. |
| 2026-10-22 | PR #246 → `34af848` (#167) | Local darwin/arm64, dev Postgres; k3d `skali-test`; GitHub CI | `go vet` + `go test` (backup with `-race`); `TestLive*` in `internal/backup` | Passed. Stalled transfers, copy timeout, read retries and scheduled retries covered by unit/DB tests; live suite exercises the real gateway. A stalled remote S3 target was not reproduced live. |
| 2026-10-22 | PR #247 → `5239758` (#145) | Local darwin/arm64; k3d `skali-test`; GitHub CI | `go vet` + `go test`; 9 MB blob PATCH at 100 KB/s through websecure to `registry:3`, before and after applying the rendered HelmChartConfig | Passed. 502 at 61s without the overlay; 202 at 87s with `readTimeout=30m`; `GET /v2/` normal. Authenticated push on khz not run. |
| 2026-10-22 | PR #248 → `7a7d7d9` (#194, #195) | Local darwin/arm64, dev Postgres; k3d `skali-test`; GitHub CI | `go vet` + `go test`; `TestLiveBucketContracts/Quota` | Passed. Seven transition tests fail with the check disabled. No live deploy of a refused change through the CLI or Studio. |
| 2026-10-22 | PR #250 → `2912a2b` (#249) | Local darwin/arm64, dev Postgres; GitHub CI | `go vet` + `go test`; Studio check/vitest/lint | Passed. Database-drop and volume-shrink refusals fail with the gate disabled; API refusal and confirmed retry end to end. No live rollback on a cluster; Studio dialog not exercised in a browser. |
| 2026-10-22 | PR #252 → `ac21753` (D1) | Local darwin/arm64, dev Postgres; k3d `skali-test` with CNPG 1.29.2 | `go vet` + `go test` over all Go packages; `TestLive*` in `internal/substrate/cnpg` and `internal/backup`; image comparison on both architectures; Trivy 0.76.1 | Passed. `TestLivePoolPatchUpdate`: 17.9 → 17.11 restarts the primary in place, data kept, server reports 17.11. Scans 17/253 and 15/163 → 2/82 critical/high. No upgrade from rc.7 rehearsed. |
| 2026-10-22 | `2912a2b` + Vitest 5.0.5 (#184) | Local darwin/arm64; source scans target all four released hosts; image scans target both Linux architectures | Go 1.26.10 / govulncheck 1.7.0; npm audits; Trivy 0.76.1; native-tool binary scans; Studio checks | Go: no affected imported packages/called symbols in 12 source scans. npm clean after Vitest patch, 80 Studio tests pass. All 86 image scans executed; 64 contain high/critical package matches. Eight native-tool scans also have symbol findings. [Review and exact evidence](dependency-review-v0.1.3.md); release remediation remains D1–D8. |
| 2026-10-22 | `v0.1.3-rc.7` (#204) | skali dev (k3d `skali-dev`, private CA) and khz (Let's Encrypt), sslip.io hostnames; Studio in headless Chromium | curl over 5 aliases × http/https × GET/POST; app scaled to 0; `skali route list/probe`; Studio routes panel, config card, run list and detail | Passed: 301 GET / 308 POST with path and query kept, POST arrives as POST; automatic alias → canonical HTTPS in one hop; optional keeps scheme; disabled and not-yet-issued aliases refuse HTTPS by strict SNI; one LE cert per alias via HTTP-01; alias at 192.0.2.1 deferred while the route activated (`1 route deferred`, `with warnings`, `DNS pending`); aliases redirect with the backend at 0 (canonical 503). Defect fixed in `c7dc0e3` (PR #254): an alias's own variable is redacted in its checkpoint and `route probe` returns an internal error. Non-default edge port not exercised. |
| 2026-10-22 | `v0.1.3-rc.8` (`b398900`, D6/D8) | Local darwin/arm64; GitHub release run 39841845436 | Trivy 0.76.1 (DB 2026-10-22 13:15 UTC) on Alpine 3.24.3, BuildKit v0.34.1, local and published skalid images; govulncheck 1.7.0 source and binary (GoReleaser v2.18.3, sqlc 1.30.0/1.31.1); `scripts/check-release-snapshot.sh`; release CLI checksum and `--version` | Passed. Published image 0/0/0/0/1 on both architectures (only the `x/crypto/openpgp` deprecation notice, not imported); zlib/musl findings gone. Release metadata verified by the workflow and locally. |
| 2026-10-23 | `v0.1.3-rc.8` (#145, #141/#150/#151/#168) | khz (rc.7 → rc.8 upgraded), released darwin/arm64 CLI; throwaway project `slowpush-check` | Authenticated deploy through a local CONNECT proxy capping uploads at 512 KiB/s; `/api/healthz` probed every 5s; slot scenarios in a second environment | Push: one 100.2 MiB registry tunnel for 209.7s, deploy succeeded, 290/290 probes 200 (max 0.2s). Slot scenarios passed except a dropped client connection, swept after 31 min. Defects: declared rollout timeout below 10m ignored (failed at 10m9s with `timeout: 1m`, pre-existing since #9); CLI claims the environment is unchanged when it could not fail the deployment; cancelled/failed upload summary reports bytes of completed requests only. All three fixed in [PR #256](https://github.com/Hinkolas/skali/pull/256) for rc.9. |
| 2026-10-23 | `v0.1.3-rc.8` upgrade from rc.7 (D1) | khz, kubectl on skali-ctrl-01 | Pool and platform-database images, instance restarts, `show server_version` | Passed. pg17-shared on 17.11 and pg18-shared on 18.6, each 2/2 ready with 0 restarts, replica before primary. Platform `skali-db` spec and status image 18.6, healthy; replica restarted 20:33:58Z, primary 20:37:22Z; server reports 18.6. |
| TBD | TBD | TBD | Full `skali dev upgrade` / cluster upgrade from rc.7 with an in-flight deployment | Not run yet. |
| 2026-10-23 | Local branch `chore/update-seaweedfs-cnpg-cert-manager` `f379889` (D4) | Local darwin/arm64, dev Postgres; k3d `skali-test` (k3s v1.36.3) | Trivy 0.76.1 on both architectures; operator upgrade 1.29.2 → 1.30.1 with a two-instance cluster holding data; `go test ./...` with DB; `task test:live` | Passed. SeaweedFS 4.48, CNPG 1.30.1, cert-manager 1.21.2; highs 23→5, 38→10, 15–17→5. Operator upgrade recreates each PostgreSQL pod (primary restarts without switchover, ~20 s); data intact. 42 live tests pass; destructive restart test not run. SeaweedFS telemetry disabled. Not pushed. |
| 2026-10-23 | `cf22d16` (#258, D4), `2bccb22` (#259, D3), `da16047` (#260, D7), `7d4afdf` (#261, D2), `ce69d5e` (#262) | Local darwin/arm64; k3d `skali-test` on k3s 1.36.5; Lima e2e VMs (server grown to 40 GiB); actual registry 2.8.3/3.1.2 images | Trivy 0.76.1 and govulncheck 1.7.0 scans; `go test ./...` with DB; `task test:live`; `task test:cluster` (Linux, stale repeat-upgrade step skipped locally for D2); `task test:cluster:darwin`; `TestDevEndToEnd`; registry migration and token scenarios | All merged for rc.9. D3 exposed a host-gateway DNS race on k3s 1.36.5 that stopped `skali dev` from creating a platform, fixed in #262. Open: node-IP-move recovery falls back to a full converge on 1.36.5 ([#263](https://github.com/Hinkolas/skali/issues/263)); stale e2e assertions (cluster repeat-upgrade, join phase; dev skalid-restart subtest); e2e suites use the developer's real CLI config, so the cluster repeat-upgrade step can reach the selected remote. |
| 2026-10-23 | `2ddaf7b` (#264) | Local darwin/arm64; freshly recreated Lima e2e VMs; darwin VM; k3d `skali-dev-e2e` | `task test:cluster`; `task test:cluster:darwin` (real `~/.config/skali` untouched); every `TestDev*` test across reruns; `go test ./cmd/skali/ ./internal/clusterstate/` | macOS cluster suite isolated from the real CLI config; all three e2e suites green again (stale checks since September fixed). Join preflight now keeps the last cause after its retry budget. `task test:dev` timeout 40 → 90 min. Still open: #263. |
| 2026-10-23 | `v0.1.3-rc.9` (`2ddaf7b`) | Local darwin/arm64; GitHub release run 39997590976 | Snapshot rehearsal; govulncheck 1.7.0; Trivy 0.76.1 on PostgreSQL 18.6 (libxml2); released `release.json`, darwin/arm64 CLI checksum and `--version`; skalid manifest list | Passed. Notes prepended to the release body. Rehearsal on khz pending (needs a fresh `skali login`). |

## Scope decisions and accepted limitations

| Date | Decision | Reason / residual risk | Follow-up |
| --- | --- | --- | --- |
| 2026-10-22 | Proposed 12-issue stabilization scope | Focus the next candidate on correctness and release evidence. | Confirm before implementation. |
| 2026-10-22 | #141 narrowed; cancelling a run before its handoff split into #243 | The handoff marker, bound adoption and the success mislabel are fixed; a cancel before handoff still does not stop the change. | #243, outside this release. |
| 2026-10-22 | #143 detects exhaustion with a connection probe; connection budgets move to PgBouncer | A probe catches refusals at the limit, not saturation by one tenant; budgets need a pooler. | #164 rescoped to PgBouncer, after v0.1.3. |
| 2026-10-22 | #143 ships without an environment-health command for project members in the remote CLI | API, `skali database` and Studio show pool connection health. | Open a follow-up if needed. |
| 2026-10-22 | #167 adds `SKALI_BACKUP_COPY_TIMEOUT` (6h) instead of reusing the 1h Job timeout | Reusing it would newly fail large bucket copies that succeed today. | — |
| 2026-10-22 | #145 sets a 30-minute read timeout on the shared `websecure` entrypoint | Traefik has no per-route read timeout; every HTTPS route gets the bound, and slow clients can hold connections that long. Documented in `docs/limitations.md`. | Configurable setting in #120. |
| 2026-10-22 | #194's plan preview refuses an unsupported transition instead of listing a classified plan row | Same error and code as open; clients see it before building. | — |
| 2026-10-22 | Rollback now runs the transition check, but still skips the destructive-change confirmation and the volume-shrink check | Pre-existing gap found while implementing #194; a rollback can drop services added since. | Tracked as [#249](https://github.com/Hinkolas/skali/issues/249) (priority high); fix before the RC. Fixed in `2912a2b` ([PR #250](https://github.com/Hinkolas/skali/pull/250)). |
| 2026-10-22 | Failed backups keep leaving unreferenced data in the target; scheduled retries can triple it | Documented in `docs/limitations.md`; cleanup needs its own race-safe design. | #159 |
| 2026-10-22 | #184 inventory completed; dependency findings expand release work | Source/npm updates do not patch bundled binaries. PostgreSQL 17.9/18.4 miss upstream core fixes; registry 2.8.3 has an end-of-support base. Other runtime/build/native findings require bounded review. No blanket risk acceptance recorded. | [D1–D8 dependency follow-ups](dependency-review-v0.1.3.md#release-follow-up-tasks), independently reviewable changes before stable. |
| 2026-10-22 | Owner schedules registry dependency remediation D2 with #242 after the next RC and before v0.1.3 stable | The next RC may retain registry 2.8.3 and its documented findings. This deferral does not carry into stable. | Superseded on 2026-10-23. |
| 2026-10-23 | Owner moves #242 out of v0.1.3; D2 becomes a plain registry image update on filesystem storage | #257 (PostgreSQL 17 only, one installer-owned system cluster, SeaweedFS metadata moved into it) changes the same bootstrap, SeaweedFS and converge code #242 depends on, and both are stateful migrations that each need their own RC. Keeping v0.1.3 feature-frozen ships the rehearsed rc.8 fixes sooner. | v0.1.3: rc.9 with #256, D4, D3, D7, D2. v0.1.4: #257 parts 1–3, then #242; #257 part 4 in the release after. |

## Final release decision

- Candidate evaluated: **TBD**
- Exact commit evaluated: **TBD**
- Selected issues verified: **TBD**
- Outstanding defects and accepted limitations: **TBD**
- Rehearsal and observation evidence: **TBD**
- Decision: **Not ready; dependency remediation from #184, #204 and candidate verification pending.**
- Tag-push authorization: **Not requested or granted by this roadmap.**
