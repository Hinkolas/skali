# Testing Skali

Use the smallest suite that exercises the changed behavior during development,
then broaden validation before merging. `task test:live` always means **all live
cluster coverage**. It is not the default inner development loop.

## Test layers

| Layer | Command | Dependencies and purpose |
| --- | --- | --- |
| Go unit tests | `task test` | Skips DB tests without `TEST_DATABASE_URL` and live tests without `TEST_KUBECONFIG`. Task loads `.env`, so configured dependencies can enable additional tests. |
| Go + control-database tests | `task test:db` | Starts the project Postgres; exercises migrations, persistence, APIs, and controller logic with fakes where appropriate. |
| Substrate unit + DB tests | `task test:substrate` | Starts project Postgres and explicitly disables live cluster tests; includes CNPG and SeaweedFS rendering/client tests. |
| Live integrations | `task test:live` | Existing disposable k3d cluster plus project Postgres; real Kubernetes controllers, PostgreSQL, SeaweedFS, network policies, and observation. |
| Docker integrations | `task test:docker` | Docker/buildx and a throwaway registry. |
| Development end to end | `task test:dev` | Real CLI workflow; creates and mutates its own `skali-dev-e2e` clusters. |
| Linux installer end to end | `task lima:up`, then `task test:cluster` | Dedicated Lima server/agent VMs with real systemd. |
| macOS installer end to end | `task test:cluster:darwin` | Creates/deletes its own VM; stop the Linux end-to-end VMs first. |
| Studio | `cd studio && npm test && npm run check && npm run lint` | Frontend tests and static checks. |

CI runs the Go suite with Postgres, vet/format/generated-file checks, applicable
frontend checks, and release checks according to changed areas. It does **not**
set `TEST_KUBECONFIG` or run the live, Docker, development, or installer suites.
A green CI result alone does not validate a changed live integration.

Each ordinary DB test creates, migrates, and drops a unique database on the
server behind `TEST_DATABASE_URL`. The bucket contract suite deliberately shares
one such database for the lifetime of its parent test. The development database
and Compose volume are never reset by these tests. `task db:stop` preserves them.
Do not remove the Compose volume to fix a migration failure.

## Live prerequisites and commands

Use the toolchain in [Local setup](development.md#local-setup), with Docker
running and `k3d` available on `PATH`. `kubectl` is useful for diagnostics.
Ordinary Go tests do not require a Studio build.

Start the pinned disposable test cluster explicitly:

```sh
task k3d:up
task test:substrate
task test:live:substrate:buckets -- -v
task k3d:down # optional; retaining the cluster retains cached images
```

Live task commands start the project Postgres, use `.k3d/kubeconfig`, and honor
`SKALI_DEV_DB_PORT` (default `55432`). They do not create or delete the k3d cluster.
For an external Postgres or another disposable cluster, use direct `go test`
with `TEST_DATABASE_URL` and `TEST_KUBECONFIG`. Never point live tests at a
production or development installation: they deliberately destroy the platform
namespace, kill workloads, and change access policies.

An unset kubeconfig skips live tests; a configured but missing/invalid file
fails. DB-backed live scenarios also skip without `TEST_DATABASE_URL`. The
network suite's `host` child requires Docker and the `k3d-skali-test` network
(override with `TEST_K3D_NETWORK`); check for that skip when using a custom
cluster. Cluster restart has a separate opt-in described below.

| Named live command | Coverage |
| --- | --- |
| `task test:live` | All four live package families below. |
| `task test:live:kube` | Apply/prune, ownership, and actual NetworkPolicy enforcement. |
| `task test:live:observe` | Kubernetes list/watch propagation, stale state, and recovery. |
| `task test:live:reconcile` | Environment deployment, healing, isolation, ownership, and rollout behavior. |
| `task test:live:substrate` | All substrate scenarios, including all six bucket contracts. |
| `task test:live:substrate:database` | User claims, system claims, pgvector, and database credential rotation. |
| `task test:live:substrate:bootstrap` | Cold object-store bootstrap and cold bucket provisioning. |
| `task test:live:substrate:buckets` | Permissions, CORS/uploads, quota, restore fence, rotation, and destructive removal on one store. |
| `task test:live:substrate:observation` | SeaweedFS usage, outage/staleness, and recovery on its own store. |
| `task test:live:substrate:network` | Platform claim-holder access, denied bystanders, host NodePorts, and revocation. |
| `task test:live:substrate:legacy` | Removal of legacy public S3 edge resources; lazily installs cert-manager. |

The database suite keeps four independently selectable fresh-fixture tests:

| Selector | Main assertions |
| --- | --- |
| `^TestLiveClaimProvisioning$` | Tenant/role/Secret provisioning, credential leak audit, idempotence, primary loss and recovery, release, and the surviving idle pool. |
| `^TestLiveSystemClaimSameSubstrate$` | System claims use the same pool, tenant, credentials, and release path without touching the installer namespace. |
| `^TestLiveVectorExtension$` | A tenant requesting pgvector can use it; a plain tenant on the same pool does not gain the extension. |
| `^TestLiveDatabaseCredentialRotation$` | Two credential rotations, overlap/retirement, actual SQL login/ownership, stable instances, leak audit, and role cleanup. |

All live tasks disable Go's result cache with `-count=1`. The full command keeps
its 90-minute watchdog; focused commands default to 30 minutes. Override with
`TIMEOUT=45m` when diagnosing a loaded machine. These are failure ceilings, not
expected runtimes. Raising one does not fix a stuck controller or cleanup.

Additional Go test flags follow `--`. They come after task defaults, so a later
`-run` **replaces** the default selector, and `-count` can request repetitions:

```sh
# Exactly one bucket behavior; still provisions and cleans its own shared fixture.
task test:live:substrate:buckets -- -v -run '^TestLiveBucketContracts$/^Quota$'

# PR 102's database rotation scenario, including both retirements and teardown.
task test:live:substrate:database -- -v -run '^TestLiveDatabaseCredentialRotation$'

# All database scenarios, then complete integration coverage.
task test:live:substrate:database -- -v
task test:live -- -v

# Exercise reuse and ordering explicitly, serially on the same cluster.
task test:live:substrate:buckets -- -v -count=2
TEST_BUCKET_ORDER=reverse task test:live:substrate:buckets -- -v
```

Go matches subtests using slash-separated regular expressions. A selected child
runs its parent fixture but not its siblings. A selector matching no child must
not provision infrastructure. `TEST_BUCKET_ORDER` accepts `normal` (default) or
`reverse`; it is a validation aid, not an alternate coverage tier. Go's
`-shuffle` shuffles top-level tests, not the explicit order of these subtests.

Do not run concurrent substrate invocations against the same cluster. The
platform namespace, service names, and NodePorts are shared. `-parallel` does
not make this safe. Use separate disposable clusters for concurrent workers.
The full live command can run other package families concurrently; their
existing namespace isolation is separate from substrate fixture sharing.

The cluster-restart recovery test has an additional existing opt-in and skips
in ordinary live runs. Run it alone, with no other cluster users:

```sh
TEST_K3D_DESTRUCTIVE=1 task test:live:reconcile -- -v -run '^TestLiveClusterRestartRecovery$'
```

This stops and restarts the test cluster container. Never set that variable for
the full suite, whose other packages would be disrupted by the restart.

## Choose coverage for a change

| Change | Development coverage | Broaden before merging |
| --- | --- | --- |
| Pure rendering, naming, quantities, policy documents | Relevant Go package; `task test:substrate` for substrate work | Live suite if a rendered resource or provider contract changes. |
| Database claims, placement, CNPG, extensions, SQL roles | `test:substrate` + `test:live:substrate:database` | Bootstrap/network suites when metadata or access is affected. |
| Bucket permissions, quota, CORS, upload cleanup, fencing, rotation | `test:substrate` + matching bucket subtest | Entire bucket suite; bootstrap for provisioning changes. |
| Store rendering, metadata credentials, operator manifests | Bootstrap + buckets | All substrate live coverage. |
| Observation and health | Unit tests + relevant observe/substrate observation suite | Reconcile recovery tests when environment health changes. |
| Network access or ownership | Kube + reconcile + substrate network suites | Complete live coverage for shared policies or ownership code. |
| Legacy-resource migration | Substrate legacy suite | Bootstrap/full substrate for shared lifecycle changes. |
| Shared controller helpers, migrations, dependency/image pins, fixture changes | Relevant unit/DB tests | Complete live coverage; development/installer suites when those paths change. |
| CLI dev/build/install behavior | Relevant Go tests and Docker/dev/installer suite | Live substrate coverage does not replace end-to-end validation. |

Selection is explicit, not inferred automatically from changed filenames. A
change to a shared dependency can affect several rows. Document which live
commands ran in the change's validation notes, including skips and failures.

## Fixture ownership and adding tests

`TestLiveBucketContracts` owns one control database, metadata tenant, and physical
SeaweedFS store. Its first selected child creates them lazily. Each child has a
fresh project/environment, claims, credentials, controller, and observed store.
Children run sequentially. Connections and explicit port-forwards close before
case cleanup releases claims through ordinary reconciliation, checks bucket,
identity, and Secret removal, and deletes environment namespaces. Once release
succeeds, cleanup removes that scenario's released claim/allocation records and
project. Only shared infrastructure and its matching control-database records
survive into the next child.

The next child verifies that no live bucket claims remain and that the shared
store and metadata claim are healthy. A cleanup/isolation failure stops the
remaining scenarios and fails the parent. A failed assertion can be followed by
other scenarios only if cleanup restored the fixture. Never share only Kubernetes
resources while giving each child an unrelated fresh control database.

Cold bucket provisioning and object-store bootstrap remain independent tests.
Database scenarios, primary recovery, store outage/recovery, network-policy
changes, and legacy upgrades keep fresh fixtures. A shared ready store must not
silently replace a test's cold-start or failure-recovery contract.

The database/store live fixtures use the local development shape: the pinned
PostgreSQL 17 image, one database instance, and an all-in-one SeaweedFS pod.
Production shape and replication rendering have separate unit/DB coverage;
these live cases do not establish multi-node failover coverage.

The CNPG operator and PriorityClasses are installed once per test process, only
when a selected scenario needs them. They remain on the disposable cluster.
Platform cleanup drains SeaweedFS workload owners/pods before CNPG clusters/pods,
then deletes the namespace. Namespace deletion otherwise leaves the resource
controllers to process an unordered cascade. Stopping SeaweedFS first removes
the metadata database's client before Postgres shuts down. Cleanup preserves
ordinary shutdown and finalizers; it does not force-delete pods or change
production defaults.

**Fixture cleanup is not the destructive-removal contract.** The
`DestructiveRemoval` scenario must still assert that releasing a claim removes
its bucket, identity, data, and Secrets while leaving the store running.
Deleting the entire fixture namespace proves none of those properties.

When adding a test:

1. Prefer unit or DB-backed fixtures for deterministic logic, error handling,
   state transitions, and permutations. Use real providers where their behavior
   is the contract under test.
2. Add ordinary bucket behavior to the shared parent and use its environment
   helper. Register cleanup immediately; do not use `t.Parallel` or manually
   destroy shared infrastructure.
3. Use a fresh fixture for bootstrap, outages, global policy mutations, or
   compatibility migrations. Add its selector to the appropriate named command
   and this coverage matrix; keep the full `^TestLive` selector inclusive.
4. Use the common bounded drivers. They preserve unsettled-work assertions,
   retry readiness/transient failures, and fail promptly on typed control-DB
   constraint/schema errors. Do not swallow reconciliation errors.
5. Retain actual provider assertions when moving a scenario. Validate it alone,
   in the parent suite, and in reversed/repeated order after fixture changes.

`testdb.New` remains the ordinary per-test helper. `testdb.NewScoped(child,
parent)` is reserved for lazy shared fixtures: setup failures belong to the
selected child and database cleanup belongs to its parent.

### Selector migration

| Previous top-level test | Replacement |
| --- | --- |
| `TestLiveBucketPermissionBoundary` | `TestLiveBucketContracts/Permissions` |
| `TestLiveBucketCORSAndUploadCleanup` | `TestLiveBucketContracts/CORSAndUploads` |
| `TestLiveBucketQuota` | `TestLiveBucketContracts/Quota` |
| `TestLiveBucketRestoreFence` | `TestLiveBucketContracts/RestoreFence` |
| `TestLiveBucketCredentialRotation` | `TestLiveBucketContracts/CredentialRotation` |
| `TestLiveBucketDestructiveRemoval` | `TestLiveBucketContracts/DestructiveRemoval` |

The old entrypoints are removed so the full suite never executes both versions.
Cold provisioning and other isolated test names remain unchanged.

## Timing, diagnosis, and interrupted runs

Save Go's event stream for reproducible measurements:

```sh
mkdir -p .cache/test-results
task test:live -- -json > .cache/test-results/live.jsonl
# Optional jq view of completed top-level packages/tests; parent timings include children.
jq -s '[.[] | select(.Action == "pass" or .Action == "fail") |
  {package: .Package, test: (.Test // "<package>"), seconds: .Elapsed}] |
  sort_by(.seconds) | reverse' .cache/test-results/live.jsonl
```

Use `-v` for readable progress or inspect `Output` events for `phase=...` messages.
They distinguish operator/shared setup, provisioning, scenario bodies, and
cleanup. Scenario-body timings can include provisioning; do not sum overlapping
parent/child or phase timings. Package event timings exclude compilation; shell
wall time also includes startup and dependency setup.

### Measured comparison

Measured on 2026-10-03 against PR 102 (`c2c4cd1`), on the same Apple M2 Pro
machine (16 GiB), Go 1.27.1, and retained `rancher/k3s:v1.36.3-k3s1` test
cluster with cached images. The baseline and both optimized complete runs began
with no platform namespace; the second optimized run retained the same cluster.
The baseline ran from an untouched checkout archive. Package times exclude
compilation and Task/Docker startup:

| Live package | PR 102 baseline | First optimized full run | Retained-cluster repeat |
| --- | ---: | ---: | ---: |
| Substrate | 53m29s | 23m59s (**55% less**) | 23m30s (**56% less**) |
| Kubernetes | 42.9s | 42.8s | 46.0s |
| Observation | 15.5s | 15.7s | 15.5s |
| Reconciliation | 47.7s | 48.0s | 48.0s |

The six former bucket contracts took 28m11s combined. Their shared replacement
took 6m26s in the complete run; normal, reversed, and repeated parent runs
ranged from 6m16s to 8m26s: **70–78% less**. Each of the six independently
selected children passed in 2m30s–2m53s,
including its own parent bootstrap and teardown. Cold bucket provisioning
remains separate and fell from 5m01s to 2m07s;
object-store cold bootstrap fell from 2m41s to 1m46s. The improvement exceeds the
40% full-substrate target without changing production grace periods, removing
assertions, or shortening the full-suite watchdog.

Phase measurements across the two optimized full runs explain the remaining cost:

| Phase | Observed time |
| --- | ---: |
| Control database creation and migrations, substrate | 0.2–0.9s |
| CNPG operator setup | 8s, once per process |
| Shared bucket setup, including store provisioning | 57–58s, once per parent |
| Cold store/bucket provisioning | 40–55s |
| Bucket scenario body, including its claim provisioning | 24–44s |
| Per-bucket-scenario cleanup and environment deletion | 10–22s |
| Platform cleanup with SeaweedFS and Postgres | 49–60s |
| Database-only platform cleanup | 15–29s |

The untouched baseline had no equivalent phase logger. Read-only sampling of
its namespace deletion timestamps (five-second resolution) measured about
112–135s for bootstrap/observation/network platform teardown and 27–30s for
sampled database-only teardown. Those namespace-first waits are compared with
the **whole ordered platform cleanup** above, including workload draining.
The six bucket contracts now pay for one store bootstrap and one platform
teardown, while preserving each scenario's claim/environment cleanup.

Normal and reversed order passed, as did every child selected independently.
A `-count=2` shared run passed all twelve child executions with exactly two
store setups, two platform teardowns, and one operator setup in its JSON log.

For planning a development loop, the focused commands measured approximately
6m35s for all four database scenarios, 3m51s for both bootstrap scenarios,
2m57s for store observation/recovery, and 3m46s for platform network access.
The kube, observe, reconcile, and legacy commands took approximately 39s, 16s,
47s, and 39s.
The complete `test:live:substrate` command also passed in 23m45s, within its
default 30-minute watchdog.
These are Go package elapsed times. The database-only run included timestamp
gaps consistent with host suspension; its shell wall time is not a comparable
benchmark. The full-run comparison above uses uninterrupted runs.

These are warm-image development measurements, not performance assertions.
Repeat comparisons on the same machine, pinned cluster, and image-cache state;
record selected tests and skips. Image pulls, load, and controller scheduling
can add substantial time. Live wall-clock times are deliberately not asserted
in ordinary tests.

Validation of this refactor covered every named command, all six bucket children
individually, normal/reversed order, two repetitions in one process, and two
complete live runs. All 38 passing test/subtest selectors from the baseline were
represented in both full runs after applying the selector migration above. The
existing opt-in cluster-restart test also passed separately. Unit/DB tests cover
cleanup ordering, absent resources, later stages after errors, guarded record
removal, and immediate rejection of permanent SQL errors while transient errors
retry. The broad DB-backed Go suite, vet, and formatting checks passed.

Temporary Go-overlay probes also exercised real partial CNPG/SeaweedFS startup
and leftover-platform cleanup. An injected bucket-scenario failure stayed visible
as a failure, while cleanup restored isolation and the subsequent scenario
passed. Parent teardown removed the platform namespace. These fault probes were
validation-only; the ordinary suite contains no deliberately failing test.

One earlier package-parallel DB-suite attempt hit the unchanged access-matrix
test's two-second HTTP deadline. That case passed twice in isolation, and the
complete DB-backed suite passed with `go test -p 1 -count=1`. Its timeout was not
changed as part of this work.

On failure, read the first scenario error and the last waiting reason before
cleanup diagnostics. Cleanup attempts subsequent stages even after a failure and
reports remaining pods, PVCs, controllers, and finalizers. Do not remove
finalizers or shorten production grace periods to obtain a green test.

For a process that stopped before it could log diagnostics, inspect the same
resources directly:

```sh
kubectl --kubeconfig .k3d/kubeconfig get namespace skali-platform -o yaml
kubectl --kubeconfig .k3d/kubeconfig -n skali-platform get \
  deployments,statefulsets,pods,pvc,clusters.postgresql.cnpg.io,databases.postgresql.cnpg.io
```

After an interrupted run, first confirm that no other test is using the cluster.
The next substrate fixture attempts ordered cleanup of the leftover platform.
If the disposable cluster cannot recover, recreate **only that test cluster**:

```sh
task k3d:down
task k3d:up
```

Normal failures run registered cleanup; a killed process or Go's process-wide
timeout may not. Such runs can also leave uniquely named `skali_test_*` control
databases. Identify ownership before removing any; a matching name alone is not
proof that another running suite has finished. Never delete the development
Postgres volume or unrelated namespaces as a test-recovery shortcut.
