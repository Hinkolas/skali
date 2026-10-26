# Versioning

The supported CLI dispatch and embedded-reference contract starts with
**v0.1.0-rc.3**. Earlier prerelease CLIs are unsupported targets. The CLI,
daemon and manifest compiler for a target use the same exact release;
manifest review and stored document schemas remain separate concepts.

## 1. Resolve one target per invocation

Selection order is `--remote`, the checkout's `.skali/target.yaml`, the current
remote, then the invoked home CLI when no target exists. `--manifest PATH`
selects another checkout for references, validation, compilation and manifest
upgrade. Unknown explicit remotes, invalid bindings, ambiguous manifests and
malformed configuration are errors. They never silently select home.

Online workflow commands probe the selected remote's `/healthz` before choosing
a CLI, even when its recorded release matches home. Health is the cross-release
route: its `Skali-Version` and `Skali-Instance` headers identify the daemon
(the edge identity route `/.well-known/skali-edge` is version-free for the
same reason, but serves the daemon's own probes, not clients).
A changed pinned installation identity requires an explicit login/trust decision.
Authenticated API requests carry `Skali-Client-Version`; the daemon rejects
released clients that do not exactly match its own release. Development builds
are an explicit maintainer path, identified as such, not a release substitute.

The launcher freezes the remote name, endpoint, pinned identity, release,
selection source and checkout binding for the invocation. It passes that
secret-free context to its child. The worker validates its own release and uses
the same target rather than resolving a new current remote or checkout binding.
A removed or replaced remote fails the command. Credentials are loaded from
protected configuration, not copied into the context. The worker marker applies
to that child only; host commands it launches can start independent invocations.

References include provenance in their human-readable output. Other dispatched
commands put selection information on stderr, keeping JSON and compiled output
on stdout clean. `skali version` reports the invoked binary; it does not identify
the binary that answered a previous reference request.

The CLI reference starts with an overview and command index. Focused reads such
as `skali skill read cli deploy` and `skali skill read cli cluster node remove`
dispatch through the same target selection and carry the same provenance.
`skali skill read cli workflows` lists cross-command guides. Older releases
that only embed a combined CLI reference keep that interface: read their
overview instead of substituting newer documentation.

## 2. Exact dispatch, offline work and the home rule

The release feed supplies binaries with published checksums. Cached entries
retain their checksum and are verified before execution. A missing, corrupted,
unfetchable or unstartable matching CLI is an error. Home never executes as a
fallback for execution or embedded references at a different target release.
Command help has the explicitly labeled recovery exception described below.

`--offline` is available for `skill read`, `validate`, `compile`, `manifest
upgrade` and dev workflows. It skips remote discovery and CLI downloads. A
configured target must have a recorded release and an available matching CLI
(home or cache). With no selected target, local work uses home. Requests to the
local dev platform remain allowed. Offline validation proves compatibility with
the selected recorded release, not the cluster's currently running release.
Reconnect and validate online before deployment.

Home, the CLI in PATH, is always at least as new as every cluster it manages;
dispatch only goes downward. A target newer than home (including a newer
prerelease) is not managed until home is upgraded to that release. On a
terminal the CLI offers that upgrade on the spot and, once accepted, continues
the command in the matching release; a non-interactive run, an `--offline`
selection, or an install directory the user cannot write stops with an error
naming `skali upgrade --version <release>` (with `sudo` where needed, or a
private install under `~/.local/bin`). Nothing is downloaded before consent,
and a cached newer release is never executed behind a stale home. `skali
upgrade` and the consented upgrade hold the same installation lock and recheck
the binary on disk inside it; a release another process already installed is
never downgraded. A failed replacement is an error, not a fallback to the cached
copy. Successful installation refreshes managed skills and completion scripts;
individual replacements are atomic. An explicit downgrade warns about every
configured remote it puts out of reach. Because home is the newest release in
use by construction, removing every remote leaves the newest release installed.

Configuration updates use a locked read-modify-write transaction. Each writer
applies its changes to the latest file, preserving unknown fields and unrelated
remotes. Conflicting logins fail rather than overwriting one another. Health and
API observations update a record only while its endpoint, login and installation
still match; delayed responses cannot resurrect a removed remote.
`remote list` and `remote remove` also accept incomplete remote entries so the
configuration can repair itself. Operational commands remain strict. Invalid YAML
is never rewritten automatically; diagnostics name the file to edit.

Cache publication and pruning share per-release locks with execution. A child
holds a shared lease until it exits. Pruning skips acquired entries and recent
publications and retains releases referenced by home or configured remotes,
including the local platform. Locks are stable sidecar files and are not deleted.

## 3. Execution and retry policies

Host installation/recovery, binary version reporting and upgrades, skill
installation, completion installation, and local platform stop/reset/status
remain with home. API-backed managed cluster upgrades use the selected remote's
release and refuse a target newer than home, whether named explicitly, chosen
by the daemon's scan, or left over from an update to finish or retry: the CLI
upgrades first, then the cluster. Remote configuration commands stay home;
their API work hands off before execution after resolving the positional
target. Local removal of a remote or token is still possible when server-side
revocation is unavailable.

Command help (`deploy --help` and `help deploy`, for example) never contacts a
cluster, downloads a CLI or upgrades home. It prefers the recorded target
release's installed/cached CLI and identifies that release as unverified. If
configuration or the matching CLI is unavailable, it displays explicitly labeled
home help without claiming target compatibility. `--offline` is accepted with
help but is unnecessary; it does not enable offline API commands. Embedded
references remain strict and never substitute another release.

Dynamic completion uses a matching installed/cached CLI without downloading or
upgrading. Missing target context or cache, or a recorded release newer than
home, produces no remote suggestions. Local completion sources such as remote
names remain available without a network request.

An observed version difference alone never causes command replay. A typed
version rejection may trigger at most one retry of explicitly read-only work,
and only toward a release not newer than home; a cluster that moved past home
stops the command with the upgrade remedy instead. Mutating command bodies are
never replayed automatically. Deployment and backup observation failures
preserve the accepted run ID and show `skali run attach`.

Independently of versions, the API client repeats a read (a GET) through
transient failures for up to about 20 seconds with growing delays: a reset or
dropped connection, the edge's 502/503/504 while skalid restarts, and a refused
connection once the server has already answered the command. Authentication,
authorization, validation, skalid's own coded errors, timeouts, and a changed
installation identity fail at once; Ctrl-C ends the wait immediately, and no
wait runs past a command's deadline. Requests of any other method are sent once.

A managed cluster upgrade is submitted once. If the daemon changes release
while `--wait` observes it, the CLI probes the same pinned installation and
hands off to the new release with only the accepted operation ID, provided that
release is not newer than home; otherwise the observation stops and the error
names `skali upgrade --version` followed by `skali cluster upgrade --wait` to
resume watching. The internal `--observe-operation` path cannot submit, resume
or retry an upgrade. It verifies that the observed operation is still the
accepted one; handoffs are bounded.

## 4. Manifest review and stored document schemas

Manifests carry no application version or review revision. Parsing and compilation
always validate the current grammar. Removed fields, including the old `skali`
and `version` fields, report an explanation and an upgrade hint.

Numbered YAML records in `internal/manifest/changes/` describe additions,
removals and semantic/default changes. Each filename supplies a consecutive
revision starting at 1. Add one record with the implementation, resolve numbering
conflicts before merging, and never modify, remove or renumber merged records.
Records are embedded directly in ordinary builds; tagging needs no ledger
preparation, stamping or generation. Application release numbers are independent.

The original grammar predates revision 1. Revisions 7–10 repair missing history
for already shipped route aliases, bucket CORS, multipart cleanup support, and
the database extension catalog. They are informational additions, do not change
runtime behavior, and do not require a new semantic acknowledgement. Older
binaries retain the ledger they shipped with; a small revision number alone
does not prove a field is unavailable. Read the target release's field reference.
The manifest reference also contains a generated copy of the entire ledger.

Product release notes are published with each [GitHub release](https://github.com/Hinkolas/skali/releases)
from Git history; they are broader than the manifest ledger. Deployment
revision IDs/checksums describe concrete deployable snapshots, not this grammar
history. The editor schema's floating `/schemas/v1/` URL follows stable; it does
not encode a manifest review revision. Pin an editor to
`https://skali.dev/schemas/<release>/skali.schema.json` for an exact release,
including prereleases, and use `skali validate` for checks beyond JSON Schema.

The local CLI stores per-manifest acknowledgements in
`.skali/manifest-review.yaml` beside the manifest, keyed by filename. This is
self-ignoring, disposable checkout state, separate from target bindings. Missing
history trusts current semantics and begins tracking after successful manifest
compilation. Later deployment or network failures do not undo that local review.
Ordinary commands do not advance existing history. Relevant changes since the
stored revision block local authoring/deployment commands, including changes to
omitted defaults; unaffected manifests continue without an acknowledgement.

`skali manifest upgrade` removes supported obsolete fields, validates and compiles
the proposed result, then updates local history. Semantic changes require review
and an explicit `--acknowledge`; the flag never bypasses validation. State-only
upgrades leave the manifest untouched. Comments, line endings and permissions
survive supported edits; unsafe YAML shapes require a manual edit. If cleanup
succeeds but saving state fails, the command reports that partial success and is
safe to rerun. Development builds use their embedded revision without `--to`.

A local revision newer than the selected compiler produces an informational
notice, not a compatibility rejection. Normal target validation still applies,
and the stored revision is never lowered. `skali skill read manifest --since N`
shows changes after revision N; 0 shows all entries.

Fresh clones, copied manifests with a new filename, and server submissions have
no inherited review history. They validate against current semantics. Local
history is guidance, not a portable compatibility guarantee: it is neither sent
to the server nor included in compiled definitions or hashes. Editing an existing
manifest retains its history. Malformed local state is an error, not missing
history; repair or explicitly delete it. Failure to save automatic first-use
history only warns, so read-only checkouts can still be validated. Explicit
upgrade reports persistence failures as errors. Help, completion, references and
metadata-only operations do not read or write review history.

Definitions and revisions have independent integer schema constants. API-facing
schema values are integers. The physical database columns remain
`schema_version TEXT`, converted at the storage boundary. Readers accept both
the legacy and current schema-1 envelopes. New stored schema-1 definitions include
`version: "1"`; revisions include `schemaVersion: "1"` and the nested definition
alias so the preceding daemon can read writes during rollout overlap. These
aliases describe storage, independently of local manifest review history. Existing
revision contents and checksums are never rewritten. No SQL rename/type-conversion
migration is needed for this unreleased redesign.

## 5. One local dev platform

There is one fixed `skali-dev` cluster and one installation record under
`$XDG_STATE_HOME/skali/dev/skali-dev/` (default state home: `~/.local/state`).
The record identifies the installed platform release, k3s image and credentials.
The local remote is dev-owned and never becomes the current deployment target.

`skali dev` and `skali dev start` use the shared target resolver. An absent
platform is created at that release. A matching platform starts or is reused.
Any other platform fails before changing anything: an older release or k3s pin
names `skali dev upgrade`, and everything else names `skali dev reset`. Switching
targets never automatically stops, upgrades, deletes or creates another
version's cluster.

### Upgrading the local platform

`skali dev upgrade` (dispatched to the target release like `skali dev start`)
moves the existing platform to that release and k3s pin in place. Before
changing anything it shows the installed and target release, skalid image and
k3s image, the platform components whose images move, the platform database's
current and target migration version, what is kept, and the expected
interruption, then asks for confirmation (`--yes` skips the question).

| From the installed platform | Upgrade |
| --- | --- |
| Older release, same or next k3s minor | supported |
| Same release, newer k3s patch, revision or next minor | supported (also for working-tree platforms) |
| Newer release or newer k3s | refused: migrations and k3s datastores do not go back |
| k3s more than one minor version older | refused: upgrade through a release that pins the minor in between |
| Released to working tree or back | refused: no comparable version; reset |
| Different edge ports, missing CA or missing cluster | refused: reset |

Refusals happen before the first change and name the way forward. The release
half of an upgrade is the bundle converge a production update runs: skalid
applies its forward-only database migrations at start and the operators move
to their new pins. The k3s half replaces the node container, because a k3d
node cannot change its image: the old container's configuration and the
files k3d and k3s wrote into it are saved under the record directory, a new
container is created on the new k3s image with the same configuration and the
same volumes and checked against the saved configuration, and only then is the
old container removed (never with its volumes). Everything the platform holds
lives in those volumes and the record directory: the platform database,
projects, environments, values, credentials, project databases, buckets,
volumes, registry artifacts, and the development CA's certificate and key.
The CA's fingerprint is unchanged, so its local trust and every dev proxy
pinned to it stay valid.

The upgrade is recorded in the installation record before its first change and
cleared after skalid answers through the TLS edge at the target. The record
already names the target release while it is set, so an older CLI refuses the
platform, and the target's own `skali dev` and `skali dev start` refuse it too
until `skali dev upgrade` finishes it; `skali dev status` shows it as
unfinished. An interrupted or failed upgrade keeps all data and the CA and
continues from where it stopped when run again. A failure while replacing the
node before the old container was removed puts the record back to the installed
release, since nothing else changed yet. The upgrade never falls back to a
reset; `skali dev reset` stays the one destructive operation, and it removes
the volumes a replaced node mounted as well.

The fixed ports, host process interception, project environments and stop/start
data retention remain. Create/start/login/stop/reset are serialized. Reset
requires explicit data-loss confirmation or `--yes`; a failed cluster deletion
retains its installation record and credentials for retry. An explicitly
confirmed reset can also delete the fixed dev cluster when its installation
record is missing; normal dev startup still refuses to adopt it. Stop reports
whether it stopped a running cluster, found it already stopped, or found its
cluster absent. Status reports the installed release without contacting a remote or fetching a CLI. API-dependent
project inspection uses an available matching CLI and explains when unavailable.

Working-tree builds may use an explicitly built development image, but cannot
use a published release image or reuse a record that mislabels one as working-tree.
Released CLIs require their release's platform image. `SKALI_DEV_CLUSTER` and port overrides
exist for isolated tests, not for automatically selecting parallel runtimes.

Records left by testing the abandoned per-release design are detected, never
adopted or silently deleted. The error lists each exact `k3d cluster delete NAME`
and record path to remove once the user has decided to discard that local data.
There is no `dev prune` command.

## 6. Embedded references and installed skill

Each supported release embeds three topics: `manifest`, `cli`, and `architecture`.
The installed `SKILL.md` is a small operational guide: establish a target, read
its references, author changes, then validate with the same target selectors.
Release-specific grammar, defaults and lifecycle behavior live in the embedded
references. Every reference, topic list and change list starts with the answering
CLI release, selected target, selection source and verified/offline/home status.

Agents must reread relevant references when the target or resolved release
changes. A reference header is evidence for that read, not a permanent version
selection for an entire agent session. Offline provenance is explicit. Reference
examples are compiled by tests; tests cover all topics and the installed shell's
lack of version-specific grammar and defaults. No documentation website, separate
bundle download, or support for earlier prerelease documentation is required.

## Validation before release

Run the full Go suite with `TEST_DATABASE_URL` set, focused race tests, Svelte
checks, real PostgreSQL legacy/current reader tests, and the gated live single
cluster test:

```sh
TEST_DATABASE_URL=postgres://... go test -count=1 ./...
go test -race ./cmd/skali ./internal/cliconfig ./internal/client ./internal/localdev ./internal/filelock
npm run check --prefix studio
TEST_SKALI_DEV=1 go test -count=1 -timeout 40m -run '^TestDevSingleReleaseEndToEnd$' ./cmd/skali
```

The live test builds two release-stamped CLI/daemon pairs, checks create/reuse,
release mismatch without mutation, stop/start retention, confirmation and reset,
then creates the second release on the same cluster. It uses isolated state,
ports and `skali-dev-e2e`. Passing package tests with service-dependent tests
skipped does not satisfy release acceptance.
