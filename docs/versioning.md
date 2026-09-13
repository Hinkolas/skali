# Versioning

Status: design agreed 2026-09-13; slice 1 (skew hint and server-side gate),
slice 2 (self-dispatch), and slice 3 (exact match everywhere but health)
implemented 2026-09-13, slice 4 (manifest watermark, change ledger, and
stored-schema split) implemented 2026-09-14, the rest not yet. On
2026-09-13, cluster-served CLI downloads and air-gapped operation were
taken out of scope (decision 2), and the bootstrap protocol number was
dropped in favor of health as the single cross-version route (decision
3). This file is the plan of record for how the CLI,
the cluster, and the manifest stay compatible across clusters that update
on their own schedule. The build order at the end lists the slices; tick
them off here as they land.

## Why

Three parts of skali change version independently:

- The cluster updates its API and console together, on request or
  automatically.
- The CLI updates only when the user runs `skali upgrade`, and nothing tells
  them a newer version exists.
- Manifests do not update at all. They are written against the schema of
  whatever release the author had at the time.

That produces three failures in practice:

- A cluster auto-updates, the CLI does not, and commands fail with errors
  that do not say why.
- A manifest written several releases ago meets a CLI where fields were
  added, removed, or changed meaning. Today an unknown field is a hard parse
  error with a generic message.
- One user targets a cluster on the latest release and another that only
  updates on request and is months behind. One CLI version cannot serve
  both.

Goal: none of these are the user's problem to manage, and the rare case
where a human has to act is explained in one sentence with one fix.

## What exists today

Facts checked against the repository on 2026-09-13, before slice 1 landed.
Where a later slice changed a fact, the change is noted in place.

- The manifest is compiled on both sides. The CLI parses `skali.yaml` and
  runs `compiler.Compile` for validate, plan, and the deploy preview; the
  deploy flow then submits the manifest source, and the server compiles it
  again with the same compiler and stores its own result. Under the exact
  match rule (decisions 1 to 3) both sides run the same release, so the
  two compilations agree. The server refused to decode any stored document
  whose version was not the one it was built with (`unsupported_schema`,
  "redeploy the environment"); since slice 4 it decodes the previous
  envelope forever.
- One version knob covered three documents. `manifest.CurrentVersion` was
  the string `"1"`; manifests, compiled definitions, and revision documents
  all carried it. Slice 4 split it: the manifest carries the `skali`
  watermark, stored documents a schema integer.
- The daemon already reports its version. Every response carries
  `Skali-Version` and the installation identity in `Skali-Instance`. The
  client records both. Only `skali remote status` shows the version, and
  nothing acts on it.
- Unknown manifest fields are fatal. `yamldoc` decodes with known fields
  enforced, so a manifest from a newer release failed on an older CLI with
  "field not found"; since slice 4 the error names the field's path and
  the ledger explains it.
- A per-release binary cache already exists for hostd under
  `~/.cache/skali/hostd/<release>/`, fetched and checksum-verified from the
  release feed. `skali upgrade` already replaces the running binary and
  restores it when verification fails.
- Local dev is one shared k3d cluster named `skali-dev`. Its skalid image is
  derived from the CLI's own release version, downgrades are refused, and a
  k3s pin change requires `skali dev reset`.
- Releases are GitHub releases with stable and beta channels, and
  `SKALI_UPDATE_FEED_URL` overrides the feed for a mirror.

## Three contracts, not three components

Because compilation happens in the CLI, the compatibility relationships are
the edges between four artifacts, and each edge gets its own rule:

    manifest  -->  CLI  -->  server API  -->  stored documents

- Manifest to CLI: rolling compatibility through a change ledger and a
  watermark (decision 4). The agent skill that writes manifests follows
  the same resolution (decision 6).
- CLI to server: exact version match, made invisible by dispatch
  (decisions 1 to 3).
- Server to its own storage: the server stays backward compatible with what
  earlier builds stored (decision 4).

The single version knob couples the first and third edges today. This
design splits them on purpose: the manifest gets an author-facing
watermark, and stored documents keep a small server-internal schema
integer.

## Decision 1: the CLI follows the cluster

The CLI that talks to a cluster is always the exact same version as that
cluster. Every skali binary can dispatch to another version, and the binary
in PATH is always the newest version in use.

The API between skali and skalid is a private wire built from one commit.
Supporting a compatibility window there is a permanent tax and buys
nothing, because the cluster already knows which version is right. Skew is
not managed, it is removed.

### Self-dispatch

There is no separate launcher artifact. The dispatcher is part of every
skali binary, the way the Go command re-execs the toolchain a module
requires. On startup:

1. Resolve the target remote: the one-shot `--remote` flag, else the
   checkout binding in `.skali/target.yaml` (found from `--manifest` or by
   discovering `skali.yml` upward), else the current remote, else none.
2. Look up the version that remote last answered with, from the `version`
   field of its config record. Every API client records it on every
   response; `remote add` and `remote login` fill it from their probe; an
   empty record costs one health round trip.
3. If that version is this binary, run the command. Otherwise run the
   cached binary for that version as a child with the same stdio, working
   directory, and process group, with `SKALI_DISPATCHED=1` in its
   environment so it never dispatches again. The parent waits, ignores the
   terminal's interrupt (the child in the same group handles it), forwards
   signals aimed at its own pid, and ends the way the child ended.
4. If the child was refused as the wrong release (the cluster updated since
   last contact), its config write already carries the new version and it
   exits with the reserved status 213, printing nothing of its own. The
   parent re-reads the record, fetches that version, and runs the command
   once more; when it cannot, it says why and runs home. A 213 with an
   unchanged record is the command's own status (`skali exec` passes a
   remote process's code through) and is returned as is. The same single
   rerun happens when home itself matched the record and was refused: the
   refusal recorded the new version, and the dispatcher runs the command
   again with it.

Steady state costs no extra round trip. Commands with no remote, such as
bootstrapping a new cluster or local dev without a target, run in the home
binary. So do the commands that manage remotes or the binary itself:
`version`, `upgrade`, `completion`, `remote`, `cluster`, `skill`, and, until
decision 5 lands, `dev`. Config-writing commands in particular stay home,
because an older writer would drop fields it does not know. The three
remote subcommands that talk to a daemon are the exception in the other
direction: `remote add`, `remote login`, and `remote status` probe health
first and then hand the rest of their work to the release the daemon
named, so a login never crosses versions (decision 3). `SKALI_NO_DISPATCH=1`
turns dispatch off, and `--verbose` prints why a command did not dispatch.

### Home is the newest version in use

The binary `install.sh` places in PATH is the home binary. It is pinned by
definition, never garbage collected, and survives removing every remote.
It is what adds remotes, logs in, and runs the upgrade.

When the CLI fetches a cluster version newer than itself, it does not only
cache it. It verifies it, replaces itself with it using the existing
restore-on-failure upgrade path (`installCLI`, shared with `skali upgrade`),
prints one line saying so (`upgraded skali v0.3.2 -> v0.4.0 (remote khz
runs skalid v0.4.0)`), and runs the cached copy for the command at hand.
From then on dispatch only goes downward, from a newer binary to an older
one. That direction is free: newer code was written knowing every earlier
format. The one remaining local cross-version read, an older cluster
binary reading a config written by newer code, is covered by one rule:
CLI config changes are additive and old readers ignore unknown fields.

If the PATH location is not writable, the newer binary stays in the cache,
the old home dispatches upward to it, and one line tells the user to run
the upgrade once with the right permissions. It works, it is simply not the
normal path.

Self-replacement from a command that is not `skali upgrade` is a state
change the user did not ask for by name. It is printed, once, in one line,
and never done silently. Development builds (`v0.0.0-dev`, git-describe)
never dispatch and are never promoted, silently: the maintainer's working
tree talks to any cluster.

### Cache and pruning

- Cached binaries live under the user's cache directory, one folder per
  release, the same layout the hostd cache uses:
  `$XDG_CACHE_HOME/skali/cli/<release>/skali` with its digest next to it.
- A version stays while any remote's record references it or it is the
  home version. When the last reference moves on (a fetch stored another
  release, a remote was removed, home was upgraded), the entry is removed,
  except entries younger than ten minutes, which another skali may still
  be about to run.
- `skali version` keeps its first line and then lists each remote's
  recorded version with `this binary`, `cached`, or `not cached`, so the
  state is never hidden.
- Adding a remote older than home costs nothing: dispatch goes downward and
  no fetch touches home.

### Server-side guard

The server rejects a released CLI whose version differs from its own with
the error code `cli_version_mismatch`, naming the required version. The CLI
sends its version in the `Skali-Client-Version` request header, and the
gate sits ahead of authentication on every route under `/v1`, login and
device authorization included; only health stays open (decision 3).
Requests without the header, which is every browser, are never compared.
This is the safety net for anyone who disables dispatch deliberately.

Development builds are exempt, not guarded: a CLI or daemon reporting
`v0.0.0-dev` or a git-describe version is never compared, on either side.
The maintainer's working tree talks to any cluster, and a working-tree
daemon accepts any CLI. Dispatch skips such builds with a warning.

The CLI still prints one stderr line after any command that observed a
released daemon of another version, naming the exact
`skali upgrade --version` (or `skali dev upgrade`) that closes the gap.
Dispatch normally closes it first; the hint remains for development builds,
`SKALI_NO_DISPATCH=1`, and a fetch that failed (a warning names why, then
the command runs at home and the gate answers).

## Decision 2: where binaries come from

The public release feed is the only source of binaries. A cluster names
the version it runs, through the `Skali-Version` header it already sends,
and the CLI fetches that release from the feed. The cluster never hands
out binaries.

An earlier version of this design had every skalid image ship the CLI for
all platforms and serve it to authenticated members, so that air-gapped
clusters and deleted releases would still work. It was built as part of
slice 2 and removed the same day (git history holds it): there is no
air-gapped use case today, published releases do not disappear, and the
price was real. Four CLI builds per image added about 55 MB compressed and
180 MB unpacked to every node, the daemon hashed them at boot, and the
download needed its own authenticated route group outside the version gate
and the request timeout, an error code, an operator switch, and a
cluster-times-feed matrix of failure messages in the dispatcher. The feed
alone has one failure matrix: the release is missing, the checksum does
not match, the feed is unreachable, or the cache is not writable, each a
single warning line followed by the command running at home.

- Trust. Downloads are verified against the release's `checksums.txt`
  before anything is written, the same check `skali upgrade` and the hostd
  fetch perform. The feed is reached over HTTPS from a pinned base URL.
  Signed checksums (build order, last item) would harden this further and
  are optional now that no third party serves bytes.
- Mirrors. Forks and enterprise mirrors point `SKALI_RELEASE_BASE` and
  `SKALI_UPDATE_FEED_URL` at their own feed, which `skali upgrade` and the
  console's update scan already honor. Dispatch reads the same override.
- One rule for the release pipeline follows: never delete the assets of a
  published release. A cluster pinned to it would strand its users on a
  warning until the cluster upgrades. Marking a bad release as a
  prerelease or noting it in the release notes is fine; removing its
  assets is not.
- What a cluster contributes is exactly its version. `remote add` and
  `remote login` record it from the probe, every response refreshes it,
  and a refusal for the wrong release carries the new one.

## Decision 3: the bootstrap surface is health, nothing else crosses versions

After decision 1, what remains cross-version is whatever a home binary
needs from a cluster before it runs that cluster's release. That is one
route: `GET /healthz`, whose `Skali-Version` header names the release to
fetch (and whose `Skali-Instance` header names the installation). Every
other route, the public login and device authorization routes included,
demands the exact release, and the commands that used to log in from home
no longer do: `remote add`, `remote login`, and `remote status` probe
health first and then hand the rest of the command to the release the
daemon named, fetching it from the feed when the cache lacks it. A login
therefore runs in the cluster's own release in both directions, an old home
against a newer cluster and the newest home against an older one.

Two contracts are frozen by this, and both only ever change additively:

- The health response: body `{"status": "ok"}`, the two headers. Fields
  and headers may be added; nothing is removed or renamed. It is one route
  and one header, small enough to hold forever, where a wider surface
  could not be.
- The release feed layout: `checksums.txt` and `skali_<goos>_<goarch>`
  under the release's download path. An older home fetches a newer release
  with the code it has, so new asset names may be added next to these but
  these stay. This sits with the rule from decision 2 that assets of a
  published release are never deleted.

An earlier version of this decision versioned a wider bootstrap surface
(health and device login) with an advertised current and minimum protocol
number and a client-sent version, and had the CLI walk a prompt-driven
outdated path when it fell below the minimum. It was dropped before being
built: the number only protected the case where the feed is unreachable
and the login protocol changed incompatibly at the same time, the daemon's
existing refusal plus the hint already name the fix in that case, and a
number cannot cover the newest home logging in to an older cluster without
keeping old encoders around, which the hand-off covers by construction.

### The outdated path

What happens when a home binary meets a cluster of another release, in
order of how little it asks of the user:

1. Dispatch fetches the release from the feed or configured mirror, caches
   it, promotes home when the release is newer (one printed line, decision
   1), and runs the command in it. No prompt, no flag.
2. When the fetch fails (release missing on the feed, checksum mismatch,
   feed unreachable, cache not writable), one warning names why and the
   command runs at home. The daemon refuses it with `cli_version_mismatch`,
   and the hint after the command names `skali upgrade --version <v>` and
   the release page on the feed, where the binary can be downloaded by
   hand.
3. The cluster's updates page in the console links the release the
   cluster runs on the feed and says how the CLI follows it, for the
   admin who wants to see what is going on.

Step 2 is reached only when the feed cannot be reached or a release was
removed against the rule, and even then the exact version and its page are
on the screen. That is as narrow as a manual fallback can be while still
existing.

## Decision 4: manifest compatibility, a watermark and a change ledger

The manifest version field is a watermark: the skali release the author
last reviewed the manifest against, written as `skali: v0.1.0-rc.3`. A
ledger of grammar changes in the compiler decides what a given manifest is
affected by. Stored definitions and revisions carry their own schema
integer, owned by the server. Built 2026-09-14.

Tying the field to the skali version is right for the semantics and wrong
as an equality gate. As a gate, every release outdates every manifest, the
warning becomes noise, and people bump the number blindly. Compose went
through that and eventually deleted the field. Rolling compatibility needs
two things instead: a record of what changed and when, and a way to know
whether the author has already seen a given change. The watermark is that
acknowledgement token.

### The watermark

- The key is `skali`, chosen 2026-09-14; it reads as "a skali v0.1.0-rc.3
  manifest" at the top of the file. The value is a release tag; the
  leading `v` is optional when writing and canonical when comparing.
- It is required, validated as a release, and compared against the ledger.
  It never enters the compiled definition, so moving it changes no
  definition hash and rolls nothing.
- The previous field, `version: "1"`, is the ledger's first removed entry.
  A manifest still carrying it fails in one line that names the
  replacement and `skali manifest upgrade`.

### The ledger

Each grammar change is a registered entry in `internal/manifest/ledger.go`:
the release it landed in, its kind, the manifest path it touches (`*`
standing for one collection key), and a message with a migration hint.
The rules fall out by kind:

| Situation                                  | Behavior                                                                                      | Severity                |
| ------------------------------------------ | --------------------------------------------------------------------------------------------- | ----------------------- |
| Field added, manifest does not use it      | Nothing. There is nothing the author needs to know.                                           | silent                  |
| Field removed, manifest uses it            | Error carrying the ledger's message and migration hint, replacing the generic unknown field.  | error                   |
| Meaning changed, manifest uses it          | Error unless the watermark is at or past the change's release. Bumping it is the acknowledgement. | error until acknowledged |
| Watermark behind, nothing used changed     | `skali validate` mentions the older review point and that nothing used changed.               | informational           |
| Manifest newer than the CLI                | Watermark above the CLI version plus an unknown field: "unknown to this release, the manifest targets a newer one". | error |

Because dispatch means the CLI reading a manifest is always the target
cluster's own version, this covers several remotes at several versions
with no extra machinery. The same manifest against an older cluster
produces the last row.

How the rules are applied: the parser reports every unknown field with its
dotted path and position (a reflective walk next to the strict decode, so
"field not found in type manifest.Application" became
`applications.api.ressources: unknown field`), and the manifest package
rewrites those diagnostics through the ledger's removed entries or the
newer-watermark rule. Validation applies the changed entries against the
set of paths the manifest wrote. The server compiles the same way, so the
API's `invalid_manifest` diagnostics carry the same text.

The ledger opens with one entry, the `version` removal in v0.1.0-rc.3.
The grammar changed exactly once between tags before that (the blue-green
rollout default in v0.1.0-alpha.6), and that change is not seeded: no
manifest can carry a watermark older than the release that introduced the
field, so the entry could never fire. Changes older than the first entry
are history, not ledger.

### Details

- `skali manifest upgrade [--to <release>]` moves the watermark to this
  CLI's release (under dispatch, the cluster's) and replaces a legacy
  `version` line on the way; a development build needs `--to`. Edits are
  line-based, so comments and formatting survive. The manifest is compiled
  afterwards, so a change the new watermark acknowledges but the manifest
  has not absorbed is reported with its hint. The `version` rewrite is the
  only mechanical rename today and lives in the command; ledger entries
  gain rewrite hooks when a second one exists.
- No deprecation system before 1.0. The ledger's removed kind covers it.
  After 1.0, deprecated is one more ledger kind that warns instead of
  erroring.
- Stored schema split. Compiled definitions carry `schema: 1`
  (`compiler.DefinitionSchema`) and revision documents `schema: 1`
  (`revision.Schema`), integers that move only when the stored shape
  changes incompatibly. Both decoders also read the envelope written up to
  v0.1.0-rc.2 (`version: "1"` and `schemaVersion: "1"` on the same shape)
  forever, and migration 00002 turns the `schema_version` text columns
  into integer `schema` columns, running in the migrate initContainer on
  the next rollout. A platform upgrade therefore keeps every stored
  document readable; `unsupported_schema` remains for a document from a
  generation the build does not know.
- One-time consequence of the split: the compiled envelope changed, so the
  first plan of every environment after the upgrade shows the definition
  as changed and creates a new revision. Rendered objects are identical,
  nothing restarts, and a `releaseCommand` runs once more because the Job
  name carries the revision checksum.
- Release guard. The ledger names releases before they are tagged, so
  `skali-schema --schema release` (run by `task release:tag` and the
  release workflow) refuses to cut a release older than the newest ledger
  entry.
- Hold the manifest grammar stable through 1.0. The ledger handles the
  small changes. A wholesale restructure would be a new grammar, not a
  ledger entry, and should not happen before 1.0.
- The `skali` skill and `schemas/skali.schema.json` keep tracking the
  compiler, as they do today; the schema's `skali` property carries the
  release pattern, and the skill's fences carry the current watermark.

## Decision 5: dev runs at the target's version, one cluster per version

Local dev is an exact replica of the target cluster's platform. The
dispatched binary picks the version, each platform version gets its own
local k3d cluster, and only one runs at a time.

Dispatch already does most of the work. If `skali dev` runs under the
binary dispatched for the project's target, it pins that version's skalid
image and that version's k3s with no dev-specific version logic at all.

### Which version

Resolution order is the checkout binding, then the current remote, then
home. The binding wins because a machine with projects bound to two
clusters should run each project's dev at its own cluster's version. The
current remote covers a new project before its first deploy. Home covers
pure local development with no remote, which stays fully supported. Dev
prints which version it chose and why on every start, and takes an
explicit override flag.

### Which cluster

- Name the cluster by version. A version has its own skalid, its own k3s,
  and its own state. No cross-version migration path exists because none is
  needed. Downgrades stop being a special case.
- Switching is stop and start. Running dev under another version stops the
  running dev cluster and starts this one. The fixed API port and the
  deterministic app ports mean two cannot run at once, and enforcing that
  is simpler than avoiding collisions.
- Prune by refcount, with confirmation. A dev cluster whose version no
  remote and no home binary uses is a prune candidate. It holds data, so
  `skali dev prune` asks before deleting.
- `skali dev upgrade` is removed. A new version is a new cluster.

Tradeoffs to state in the release notes: today an upgrade carries dev
databases forward, and under this model dev data belongs to a platform
version. That is the correct semantics for a replica, and dev data is
disposable by contract, but it is a visible behavior change. Disk is the
other cost: each cluster carries its own k3s and platform images, likely
two to three gigabytes. Sharing layers through a local mirror is possible
later and is not part of the first pass.

## Decision 6: the skill follows dispatch

The installed skill is a version-neutral shell plus the architecture guide.
Version-bound references (manifest fields, CLI commands) are served by the
CLI through `skali skill read <topic>`, run from the project directory. That
command dispatches like every other one, so the binary that answers prints
its own embedded reference, and the version is right by construction.

Today the skill is four embedded files, `SKILL.md` plus `architecture.md`,
`manifest.md`, and `cli.md`, written to the agent's skills directory with a
managed marker, and a test compiles every manifest fence against the
current compiler.

Why not per-version folders on disk: the agent would have to run a version
command and then pick the matching folder, which it can skip or misread;
the folders would need regenerating whenever the set of versions in use
changes, and a swap during a running session is exactly the annoyance
agents' skill loading produces; and either the newest binary embeds every
older reference or the home binary collects them from cached binaries,
which is more machinery than the content deserves.

### The shell

`SKILL.md` says three things and rarely changes:

- What skali is and the invariants that do not move between releases:
  scratch filesystem, credentials as typed outputs, exactly one of `image`
  or `build`, and the like.
- How to get the references: run `skali skill read manifest` or
  `skali skill read cli` from the project directory. Do not guess fields.
- `skali validate` is the authority. Run it before finishing. Under
  dispatch it runs at the target's version, and with the ledger from
  decision 4 its errors name what changed and how to fix it. Even if an
  agent reads the wrong reference, the cost is one validate round trip
  with a precise message, not a broken deploy.

### Details

- Embedded content is organized as neutral versus version-bound, so the
  shell stays stable across home promotions. Only the version-bound files
  need to compile in the lockstep test.
- Auto-reinstall on home change. When `skali upgrade` runs or home is
  promoted to a newer version, the skill is reinstalled for every agent
  where the managed marker is already present.
- `skali skill read manifest --since <version>` renders what changed from
  the ledger for a manifest with an old watermark, the same data
  `skali manifest upgrade` consumes.
- If a target agent can only read files, the fallback is per-version
  folders on disk with the home version's references written as the
  default and labeled with their version. Not built until needed.

## Rejected alternatives

- A CLI-to-server compatibility window. Permanent maintenance for a private
  wire whose correct version the cluster already knows. This is the tax
  kubectl and Docker pay.
- A separate launcher binary. One more artifact with its own release
  cadence and its own compatibility story. Self-dispatch in every binary
  gives the same behavior without it.
- A frozen surface wider than health. Frozen contracts break eventually
  and then break badly. Making home the newest version and handing logins
  to the cluster's release leaves exactly one route and one header to
  freeze, which is small enough to hold.
- A bootstrap protocol number with an advertised minimum. Insurance for
  the intersection of an unreachable feed and an incompatible login
  change, blind to the newest home logging in to an older cluster, and a
  second version concept next to the release itself (decision 3).
- The cluster as a binary source, authenticated or not. Built and removed
  in one day (decision 2): no air-gapped use case, releases do not
  disappear, and the image size, boot work, extra route group, and second
  failure matrix were not worth insuring against either.
- The manifest version as an equality gate against the skali version.
  Produces churn and blind bumps and does not answer the real question,
  which is whether this manifest uses anything that changed.
- Switching one dev cluster between versions in place. Needs bidirectional
  platform migrations, an image re-import on every flip, and a reset
  whenever the k3s pin differs.
- Per-version skill folders on disk chosen by the agent. Stale between
  regenerations, swapped under running sessions, and dependent on the agent
  picking the right folder. Serving references through the dispatched CLI
  removes all three.
- Running dev clusters for several versions in parallel. Collides on the
  fixed API port and costs gigabytes of memory per cluster for a case that
  is rare in practice.

## Build order

Each slice is useful on its own and none depends on a later one.

- [x] Act on the version header the CLI already receives: a one-line hint
      on every command when skew is detected, and the server-side mismatch
      error code naming the required version.
- [x] Self-dispatch: per-remote version records, the per-release cache
      fed from the release feed, refcount pruning, home promotion to the
      newest version, and the re-dispatch on a changed server version.
- [x] Exact match everywhere but health: the login and device routes join
      the version gate, `remote add`, `remote login`, and `remote status`
      hand off to the cluster's release after their probe, the skew hint
      names the release page, and the console updates page links the
      release the cluster runs.
- [x] Manifest watermark and change ledger: the `skali` watermark replaces
      `version`, the ledger with its first entry, unknown fields with
      paths, validate's review note, `skali manifest upgrade`, the
      stored-schema split with legacy decoders and migration 00002, and
      the release guard.
- [ ] Dev per version: cluster names carrying the version, one-running
      enforcement, prune with confirmation, removal of `skali dev upgrade`.
- [ ] Skill follows dispatch: `skali skill read <topic>` with the neutral
      versus version-bound split, the slimmed `SKILL.md` shell, reinstall on
      home change, and the ledger-fed `--since` flag.
- [ ] Signed checksums, optional hardening: a signature per release and an
      embedded public key, applied to both the CLI dispatch fetch and the
      existing hostd fetch.

## Open questions

- Dev data carry-over. Whether a backup-and-restore path into a
  new-version dev cluster is worth offering, or whether the disposable
  contract stands on its own.
- Signing tooling. Whether signed checksums are adopted at all now that
  the feed is the only source, and if so which scheme and how key rotation
  is handled given the key is embedded in the CLI.
