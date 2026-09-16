# Versioning redesign: v0.1.0-rc.3 contract

- Commands verify the selected cluster and run its exact CLI release. The
  installed CLI is always at least as new as every cluster it manages: a newer
  target (including a prerelease) requires `skali upgrade --version` first,
  offered interactively on a terminal and otherwise reported as an error.
  Failed execution/reference dispatch is an error; a different home release
  never substitutes. Command help stays available without a cluster, preferring
  cached target help and identifying any fallback to home help.
- References, validation, compilation and manifest upgrade share `--remote`,
  `--manifest` and explicit `--offline` selection. References identify their
  answering release and verification status.
- Local development uses one `skali-dev` cluster. Switching platform releases
  requires `skali dev reset`, which deletes local data after confirmation.
  Stop/start retains data. Confirmed reset also recovers the fixed cluster
  when its installation record is missing. `dev prune` and `dev upgrade` are unavailable.
  Abandoned per-release records require the cleanup instructions printed by CLI.
- The installed agent skill is an operational guide. Release-specific manifest,
  CLI and architecture references are embedded in the matching binary.
- Newer manifest watermarks are rejected by older released compilers. Manifest
  upgrades validate before writing and leave semantic review to the author.
- Remote listing/removal can repair incomplete configuration entries. Invalid
  YAML reports its file path and is never rewritten automatically.
- Configuration, CLI upgrade and cache operations are serialized. `skali
  cluster upgrade` refuses a target newer than the installed CLI. Managed
  upgrade waiting continues with the accepted operation ID across release
  changes below the installed CLI; mutating commands are never automatically
  replayed.
- Existing `schema_version` text columns and legacy stored-document aliases remain
  readable during daemon rollout overlap. Existing revisions and checksums are
  unchanged. This revises an unreleased migration; do not apply the abandoned
  SQL column conversion.

Earlier prerelease CLIs are outside the supported dispatch/reference baseline.
See [versioning.md](versioning.md) for the full contract and release validation.
