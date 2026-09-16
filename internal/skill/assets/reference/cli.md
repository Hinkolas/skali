# Working with the skali CLI

The CLI is self describing: run `skali --help` and `skali <command> --help`
for the available surface. Help is always local and prefers the target's recorded
release when its CLI is available. Its header identifies the answering release
and any fallback to home help; it does not verify target compatibility.
`--offline` is accepted with help but unnecessary. Use `skill read` for
authoritative target references; those never fall back.

References, validation, compilation and manifest upgrades select `--remote`,
then the checkout binding, then the current remote. `--manifest PATH` selects
another checkout. Online commands verify the target release; `--offline`
uses its recorded release and an available matching CLI without remote discovery
or CLI downloads. With no target, the invoked CLI answers. Read the provenance
header and reread references when the target or release changes.

- `skali validate` parses and validates with file, line, column and path errors.
  A newer review watermark is rejected by an older released compiler.
- `skali manifest upgrade` proposes safe mechanical edits and a review-point
  advance, then validates before writing. Semantic changes requiring review
  leave the source unchanged. Read the diagnostic, review the behavior and
  deliberately edit the watermark to acknowledge it. A released CLI can certify
  only its own release; working-tree builds require explicit `--to`.
- `skali compile` prints the compiled definition, including defaults and
  normalized units. Version-selection context goes to stderr.
- `skali skill read manifest`, `skali skill read cli` and `skali skill read
  architecture` serve references embedded in the matching CLI. Bare `skali skill
  read` lists topics; `skali skill read manifest --since <release>` lists changes.
  Every response identifies the answering release, target, source and mode.

Offline validation does not verify the cluster's currently running release.
Failed reference dispatch never permits substituting another release's documentation.

`skali completion install` puts shell completions in place for the login
shell (`install.sh` already does this); values such as environments, remotes,
run ids, and manifest commands then complete on tab.

`skali remote list` displays incomplete entries and `skali remote remove NAME`
can remove them. Invalid YAML diagnostics identify the configuration file to edit.

## Local development

- `skali dev` is the paved path: it ensures the disposable local platform
  and deploys the project. Applications with a `dev:` block are not built;
  their dev command runs on this machine with the real resolved
  environment, and the cluster's routes reach the host process on
  auto-allocated ports (injected as `SKALI_PORT_<NAME>` and `PORT`,
  usable as `${PORT}` in the dev command). Ctrl-C pauses the project;
  `d` detaches (host dev processes still stop).
- `skali dev --preview` deploys everything in the cluster, exactly like a
  remote deploy would, ignoring dev blocks.
- One fixed `skali-dev` platform uses the selected target's release. An absent
  platform is created; a matching platform is reused. A different release or k3s
  pin fails before changing it. Run `skali dev reset` explicitly to discard its
  data and create the newly selected release. Working-tree/release transitions
  also require reset. `skali dev --remote <name>` selects another target.
- `skali dev stop` retains data; `skali dev start` restarts it. Stop, reset and
  platform status work without the remote or a CLI download. Status identifies
  the installed release. Stop distinguishes stopped, already stopped, and absent
  clusters. Confirmed reset can delete the fixed cluster even if its installation
  record is missing. There is no `dev upgrade` or `dev prune`.
- `skali dev run <name>` runs a named command from the manifest
  (`applications.<app>.commands`) on this machine with the application's
  resolved environment; `skali dev run <app> -- <command>...` runs a raw
  command; bare `skali dev run` lists the declared commands. Database and
  bucket addresses are rewritten to the local platform's loopback ports,
  so seeds and migrations just work.
- `skali dev exec [app]` opens a shell inside the running container
  instead; `skali run` manages journal runs, not project commands.

## Backups

- `skali backup target set --endpoint <url> --bucket <name> --access-key <id>`
  (admin) configures the external S3 location every snapshot goes to;
  `show` and `unset` read and remove it. Nothing is backed up without it.
- `skali backup create [--environment <name>]` takes a manual snapshot of
  every database, bucket, and volume of the environment; the run streams
  like a deploy. Manual snapshots are kept until removed.
- Manifest `backups` policies need no command: skalid snapshots every active
  environment on the cron schedule (UTC) and deletes that policy's snapshots
  once they pass its retention. The runs carry actor `schedule:<policy>`.
- `skali backup list` shows every snapshot of the project with its
  environment and origin (`manual` or `<policy> (scheduled)`); `--environment`
  filters. `skali backup restore [<snapshot-id>]` restores into the
  environment the snapshot came from (or `--environment <other>`) after a
  typed confirmation; the environment stops while data is written and stays
  down if the restore fails. `skali backup remove <snapshot-id>` deletes a
  snapshot for good. Restore and remove need `maintain` on the environment
  and a recent login.

## Routes

- `skali route list` shows each route of the environment with its URL,
  certificate state, and the reconciler's last verdict on whether the
  domain reaches this installation (`cert deferred · domain not pointing
  here yet` while DNS is elsewhere). `skali route probe` checks the domains
  right now and prints one line per resolved address (answered by this
  installation, by another server, or not at all); a domain that answers
  here counts as arrived and the reconciler requests a fresh certificate
  issuance without waiting for its cadence or cert-manager's backoff. Both
  take `--environment <name>` and `--remote <name>`; probe needs `deploy`
  on the environment.

## Access

- `skali access list` shows who holds which role on the project: the project
  role per member and the effective role per environment (`*` marks an
  explicit per-environment role). `skali access set <email> <role>` grants
  or changes a project role (read, deploy, maintain, admin); with
  `--environment <name>` it sets an explicit role on that environment only
  (`none` locks it). `skali access remove <email>` removes a member.
- `skali env list` lists environments with your access, priority, deploy
  policy, and ceiling; `skali env create <name>`, `skali env set
  --environment <name> --max-role read --deploy-policy promote-only`, and
  `skali env remove <name>` (purge) configure them. Writes need a recent login;
  the CLI asks for the password when it has aged.
- A deploy needs `deploy` on the environment for an unchanged definition and
  `maintain` to change the definition or stage values; refusals name the
  required role. A promote-only environment refuses direct deploys and
  names the way in: `skali deploy --from <source> --environment <name>`
  promotes a source's active revision (sources limited by `--promote-from`).
  Environment admins may force a direct deploy with
  `skali deploy --bypass-protection`; it asks for the password when the
  login has aged and the run records the bypass.
