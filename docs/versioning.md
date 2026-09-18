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

A managed cluster upgrade is submitted once. If the daemon changes release
while `--wait` observes it, the CLI probes the same pinned installation and
hands off to the new release with only the accepted operation ID, provided that
release is not newer than home; otherwise the observation stops and the error
names `skali upgrade --version` followed by `skali cluster upgrade --wait` to
resume watching. The internal `--observe-operation` path cannot submit, resume
or retry an upgrade. It verifies that the observed operation is still the
accepted one; handoffs are bounded.

## 4. Manifest review and stored document schemas

The required `skali` field records the release against which an author reviewed
a manifest. It is excluded from compiled definitions and their hashes. It does
not select the remote or compiler. An older watermark is acceptable when no
affected semantics changed. A watermark newer than a released compiler is
rejected by both CLI and server, naming both releases. Targeting an older release
requires deliberate review of that release and an explicit watermark edit.

The small change ledger records additions, removals and exceptional semantic
changes. Changed entries report every affected location, including omitted
fields whose defaults changed. It is not a general compatibility engine.

`skali manifest upgrade` evaluates changes against the original review point.
It proposes safe mechanical edits (including replacing legacy `version: "1"`
and dropping removed top-level blocks such as `backups:`) and ordinary
review-point advances, then validates and compiles before atomically
replacing the source. Semantic changes requiring author review produce diagnostics
and leave the file unchanged. Manual review and watermark editing acknowledge
those changes. Comments and formatting survive supported edits; unsupported YAML
rewrite shapes fail without touching the file.

A released compiler accepts `--to` only for its own release. Working-tree builds
require explicit `--to` and report their development status; they do not certify
the contents of a different published compiler.

Definitions and revisions have independent integer schema constants. API-facing
schema values are integers. The physical database columns remain
`schema_version TEXT`, converted at the storage boundary. Readers accept both
the legacy and current schema-1 envelopes. New stored schema-1 definitions include
`version: "1"`; revisions include `schemaVersion: "1"` and the nested definition
alias so the preceding daemon can read writes during rollout overlap. These
aliases describe storage, independently of the manifest watermark. Existing
revision contents and checksums are never rewritten. No SQL rename/type-conversion
migration is needed for this unreleased redesign.

## 5. One local dev platform

There is one fixed `skali-dev` cluster and one installation record under
`$XDG_STATE_HOME/skali/dev/skali-dev/` (default state home: `~/.local/state`).
The record identifies the installed platform release, k3s image and credentials.
The local remote is dev-owned and never becomes the current deployment target.

`skali dev` and `skali dev start` use the shared target resolver. An absent
platform is created at that release. A matching platform starts or is reused.
A different release, incompatible k3s pin, or transition between released and
working-tree platforms fails before changing the platform and names
`skali dev reset` as the remedy. Switching targets never automatically stops,
upgrades, deletes or creates another version's cluster.

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
There is no `dev prune` or `dev upgrade` command.

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
npm run check --prefix web
TEST_SKALI_DEV=1 go test -count=1 -timeout 40m -run '^TestDevSingleReleaseEndToEnd$' ./cmd/skali
```

The live test builds two release-stamped CLI/daemon pairs, checks create/reuse,
release mismatch without mutation, stop/start retention, confirmation and reset,
then creates the second release on the same cluster. It uses isolated state,
ports and `skali-dev-e2e`. Passing package tests with service-dependent tests
skipped does not satisfy release acceptance.
