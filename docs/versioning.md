# Versioning

Status: design agreed 2026-09-13, nothing implemented yet. This file is the
plan of record for how the CLI, the cluster, and the manifest stay
compatible across clusters that update on their own schedule, including
clusters that never see the public release feed. The build order at the end
lists the slices; tick them off here as they land.

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
- One user targets a public cluster on the latest release and an
  air-gapped cluster months behind it. One CLI version cannot serve both.

Goal: none of these are the user's problem to manage, and the rare case
where a human has to act is explained in one sentence with one fix.

## What exists today

Facts checked against the repository on 2026-09-13.

- The manifest is compiled in the CLI, not on the server. The deploy flow
  parses `skali.yaml`, runs `compiler.Compile`, and sends the compiled
  definition. The server stores that definition and refuses to decode any
  stored document whose version is not the one it was built with
  (`unsupported_schema`, "redeploy the environment").
- One version knob covers three documents. `manifest.CurrentVersion` is the
  string `"1"`; manifests, compiled definitions, and revision documents all
  carry it.
- The daemon already reports its version. Every response carries
  `Skali-Version` and the installation identity in `Skali-Instance`. The
  client records both. Only `skali remote status` shows the version, and
  nothing acts on it.
- Unknown manifest fields are fatal. `yamldoc` decodes with known fields
  enforced, so a manifest from a newer release fails on an older CLI with
  "field not found".
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

1. Resolve the target remote: the checkout binding in `.skali/target.yaml`,
   else the current remote, else none.
2. Look up the version that remote last answered with, from a small
   per-remote record the CLI keeps.
3. If that version is this binary, run the command. Otherwise exec the
   cached binary for that version, with an environment marker so the child
   never dispatches again.
4. If the child observes a different `Skali-Version` than expected (the
   cluster updated since last contact), it exits with a reserved code. The
   parent fetches the new version and re-dispatches once.

Steady state costs no extra round trip. Commands with no remote, such as
bootstrapping a new cluster or local dev without a target, run in the home
binary.

### Home is the newest version in use

The binary `install.sh` places in PATH is the home binary. It is pinned by
definition, never garbage collected, and survives removing every remote.
It is what adds remotes, logs in, and runs the upgrade.

When the CLI fetches a cluster version newer than itself, it does not only
cache it. It verifies it, replaces itself with it using the existing
restore-on-failure upgrade path, prints one line saying so, and execs it.
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
and never done silently.

### Cache and pruning

- Cached binaries live under the user's cache directory, one folder per
  release, the same layout the hostd cache uses.
- A version stays while any remote references it or the user pinned it by
  hand with `skali upgrade --version`. When the last referencing remote
  moves on, the binary is removed.
- `skali version` lists the home version and each remote's version, so the
  state is never hidden.
- Adding a remote older than home costs nothing: dispatch goes downward and
  no fetch touches home.

### Server-side guard

The server still rejects a mismatched CLI with a specific error code that
names the required version. This is the safety net for development builds,
which report `v0.0.0-dev` and skip dispatch with a warning, and for anyone
who disables dispatch deliberately.

## Decision 2: where binaries come from

A cluster serves its own CLI to authenticated members and is the primary
source for cluster versions. The public release feed installs and upgrades
the home binary and acts as the fallback source.

The cluster is the only source guaranteed to have the exact matching
version. It works air-gapped, and it does not depend on GitHub retaining
old releases. The skalid image therefore ships the CLI for every supported
platform, roughly 60 MB across four platform builds, which is acceptable on
a control-plane image.

- Members only. The download endpoint requires a valid session. There is no
  anonymous download. The version is already visible pre-auth through the
  health headers, and nothing more is exposed.
- Offline trust. Each release publishes a signed checksums file and every
  CLI embeds the public key. A binary served by a cluster is verified
  against that signature before it is written anywhere. The cluster is a
  mirror, not a trust anchor. The same verification upgrades the existing
  hostd fetch.
- Operator switch. Serving the CLI is a cluster setting. An operator who
  distributes binaries another way can turn it off, and the CLI's error
  then names the version and points at the console.
- The feed keeps two jobs: installing and upgrading home, and standing in
  as a source when a cluster does not serve downloads. The existing feed
  URL override covers enterprise mirrors.

## Decision 3: the bootstrap surface is versioned, not frozen

After decision 1, exactly one old-to-new contract remains: a home binary
talking to a newer cluster before it has fetched that cluster's CLI. That
surface is health, device login, and the authenticated download. Nothing
else crosses versions.

The server advertises its current bootstrap version and the minimum it
still accepts. The client sends its own. Additive changes never bump the
number. Old bootstrap versions stay supported for a long window, which is
cheap for three endpoints, and dropping one is a deliberate release-note
decision rather than an accident.

### The outdated path

Health is the carrier of the signal, so its shape is the one thing that
must not change. A client below the minimum can always read it and say
plainly that this CLI is older than the cluster accepts. Then it tries
fixes in order of how little they ask of the user:

1. Upgrade home from the release feed or configured mirror, then
   re-dispatch and run the original command. One prompt in a terminal, a
   plain error in scripts.
2. Point at the cluster's updates page, which offers the CLI download to a
   logged-in member through the web login. Works air-gapped.
3. Name the exact version and leave installation to the operator's own
   distribution.

Step 3 is reached only by a client old enough to fall below a deliberately
dropped floor, on a machine that cannot reach a feed, whose user does not
open the console. That is as narrow as a manual fallback can be while
still existing.

## Decision 4: manifest compatibility, a watermark and a change ledger

The manifest version field becomes a watermark: the skali release the
author last reviewed the manifest against. A ledger of schema changes in
the compiler decides what a given manifest is affected by. Stored
definitions and revisions get their own schema integer.

Tying the field to the skali version is right for the semantics and wrong
as an equality gate. As a gate, every release outdates every manifest, the
warning becomes noise, and people bump the number blindly. Compose went
through that and eventually deleted the field. Rolling compatibility needs
two things instead: a record of what changed and when, and a way to know
whether the author has already seen a given change. The watermark is that
acknowledgement token.

### The ledger

Each schema change is a registered entry in the compiler: the release it
landed in, its kind, the manifest path it touches, and a message with a
migration hint. The rules fall out by kind:

| Situation                                  | Behavior                                                                                      | Severity                |
| ------------------------------------------ | --------------------------------------------------------------------------------------------- | ----------------------- |
| Field added, manifest does not use it      | Nothing. There is nothing the author needs to know.                                           | silent                  |
| Field removed, manifest uses it            | Error carrying the ledger's message and migration hint, replacing the generic unknown-field.  | error                   |
| Meaning changed, manifest uses it          | Error unless the watermark is at or past the change's release. Bumping it is the acknowledgement. | error until acknowledged |
| Watermark behind, nothing used changed     | `skali validate` mentions the older review point and that nothing used changed.               | informational           |
| Manifest newer than the CLI                | Watermark above the CLI version plus an unknown field: "unknown to this release, the manifest targets a newer one". | error |

Because dispatch means the CLI reading a manifest is always the target
cluster's own version, this covers several remotes at several versions
with no extra machinery. The same manifest against an older air-gapped
cluster produces the last row.

### Details

- Rename the field. The current value `"1"` cannot be told apart from
  1.0.0. Pre-1.0, rework beats compatibility, so the field is renamed and
  takes a release version. The exact name is an open question.
- No deprecation system before 1.0. The ledger's removed kind covers it.
  After 1.0, deprecated is one more ledger kind that warns instead of
  erroring.
- `skali manifest upgrade` rewrites the watermark and applies mechanical
  renames from the ledger. Cheap once the ledger exists.
- Stored schema split. After a platform upgrade the new server must decode
  definitions compiled by the previous CLI version. Stored definitions and
  revisions stop sharing the manifest knob and carry a small integer, with
  old decoders kept or a migration run in the upgrade job. Today's 409 with
  "redeploy the environment" is acceptable for one user and wrong for a
  fleet with clusters months apart.
- Hold the manifest grammar stable through 1.0. The ledger handles the
  small changes. A wholesale restructure would be a new grammar, not a
  ledger entry, and should not happen before 1.0.
- The `skali` skill and `schemas/skali.schema.json` must keep tracking the
  compiler, as they do today; the ledger becomes one more thing the
  lockstep test covers.

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
- A frozen dispatch and bootstrap surface. Frozen contracts break
  eventually and then break badly. Making home the newest version removes
  most cross-version reads, and versioning the rest with a floor gives a
  planned way out.
- Unauthenticated CLI downloads from the cluster. Rejected in favor of
  member-only downloads plus the console page for humans. The bootstrap
  protocol makes authenticated downloads possible for any client version.
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

- [ ] Act on the version header the CLI already receives: a one-line hint
      on every command when skew is detected, and the server-side mismatch
      error code naming the required version.
- [ ] Self-dispatch: per-remote version records, the per-release cache,
      refcount pruning, home promotion to the newest version, the
      re-dispatch on a changed server version, cluster-served authenticated
      downloads, and the operator switch.
- [ ] Bootstrap protocol version: advertised current and minimum,
      client-sent version, and the three-step outdated path including the
      console download page.
- [ ] Manifest watermark and change ledger: field rename, ledger entries for
      every change since the last release, validate messaging,
      `skali manifest upgrade`, and the stored-schema split with old
      decoders or an upgrade-job migration.
- [ ] Dev per version: cluster names carrying the version, one-running
      enforcement, prune with confirmation, removal of `skali dev upgrade`.
- [ ] Skill follows dispatch: `skali skill read <topic>` with the neutral
      versus version-bound split, the slimmed `SKILL.md` shell, reinstall on
      home change, and the ledger-fed `--since` flag.
- [ ] Signed checksums: a signature per release and an embedded public key,
      applied to both the CLI dispatch fetch and the existing hostd fetch.

## Open questions

- Name of the watermark field. Something like `skali: 0.4.0` reads
  naturally, but the key has not been chosen.
- Bootstrap support window. How long an old bootstrap version stays
  accepted, and whether the pre-1.0 window is shorter.
- Image size budget. Whether shipping all CLI platforms in every skalid
  image is acceptable long term, or whether a per-arch split or a separate
  assets image is preferable.
- Dev data carry-over. Whether a backup-and-restore path into a
  new-version dev cluster is worth offering, or whether the disposable
  contract stands on its own.
- Signing tooling. Which signing scheme the release pipeline adopts, and
  how key rotation is handled given the key is embedded in the CLI.
