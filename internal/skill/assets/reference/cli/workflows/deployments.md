# Deployments, values, and stored revisions

Read with `skali skill read cli workflows deployments`.
[CLI overview and command index](../../cli.md).

`skali validate` reports manifest errors with file, line, column, and field
path. Local review history gates relevant semantic changes. Missing history
trusts current semantics and starts tracking after successful compilation.
`skali manifest upgrade` validates safe removals before editing the manifest
and updates local `.skali/` review history; semantic changes leave both untouched
unless reviewed and confirmed with `--acknowledge`. Development builds use
their embedded revision too; there is no `--to` option. Read changes with
`skali skill read manifest --since <revision>`.

`skali plan` previews a deployment; it does not build images, stage values in
the store, or start a rollout. It submits the candidate definition and can
record local manifest review/env-file selection. An env file is checked locally;
the server's plan still uses stored values until deployment stages the file.
`skali deploy` validates, plans, asks for
confirmation, builds/imports artifacts locally, uploads them, and starts a
server-owned run. Both select the remote and environment from the checkout
binding unless overridden. First use can prompt to create/select a project
and environment. `--environment` names the target; the manifest's name is a
project name, not an environment. There is no universal `--project` flag.

- `--env-file PATH` stages the referenced keys for this run; `--pick-env-file`
  asks again and remembers a choice. They cannot be combined. An existing
  remembered file or stored-value choice applies otherwise. In unattended
  use, supply an env file or establish the remembered choice first when
  several files make selection ambiguous. `skali values` lists names and
  versions; it has no `set`, `get`, or secret-printing subcommand. To change
  values through the CLI, deploy with an env file. `--prune-values` removes
  values the candidate definition no longer references.
- `--yes` approves a non-destructive plan; it does not approve data loss.
  An unattended destructive deploy also needs `--allow-destructive`.
  `--force` restarts applications even when the revision is unchanged;
  `--rebuild` bypasses artifact/cache reuse and implies force. Neither bypasses
  environment protection. `--build` accepts only `auto` and `local` today.
- `--detach` still performs local builds/uploads, then returns once the
  server owns the run. Keep that terminal alive through the handover. An
  accepted run can be observed with `skali run attach <run-id>`; an observation
  error is not a reason to submit the deployment again.
- `skali plan --from staging` and `skali deploy --from staging` promote the
  source environment's active definition and image artifacts into the target.
  Values belong to the target environment; source secrets are not copied.
  Promotion uses stored target values unless an explicit `--env-file` is
  supplied; it does not consult the remembered file or `--pick-env-file`.
  Promotion reads no local manifest and does not build, so `--from` cannot be
  combined with `--manifest`, `--build`, `--platform`, or `--rebuild`.
- `skali rollback` selects a previous stored revision (`--revision` takes an
  ID or unique checksum prefix); it uses that revision's definition, artifacts,
  and value versions without building or creating a revision. This is not a
  database rollback or data restore: migrations and writes are not undone.
  Removing a database or bucket through a rollback is still destructive.

`skali compile --target definition` prints the normalized definition and hash.
Version-selection context goes to stderr.
`--target revision` builds a local revision document from prepared artifacts
and resolved value versions; it does not deploy it. `--target kubernetes`
renders objects, with `--namespace` overriding the default project namespace.
For the last two targets, `--image app=reference` is repeatable and supplies an
already prepared image for each build application; `--env-file` resolves
project values. This is authoring/inspection tooling, not a replacement for
the managed deploy flow.

Manifest review revision (an integer in local `.skali/`), deployment revision
(a stored ID/checksum), and the CLI/daemon release (a version tag) are separate.
`manifest upgrade` updates the first; `rollback --revision` selects the second;
`upgrade --version` installs the third.
