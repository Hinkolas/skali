# Working with the Skali CLI

Use the workflow notes below for operational decisions and the
[complete command and flag reference](#complete-command-and-flag-reference)
for exact syntax, arguments, aliases, defaults, and every supported flag.

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
  Local review history gates relevant semantic changes. Missing history trusts
  current semantics and starts tracking after successful manifest compilation.
- `skali manifest upgrade` validates safe removals before editing the manifest
  and updates local `.skali/` review history. Semantic changes leave both untouched
  unless explicitly reviewed and confirmed with `--acknowledge`. Development
  builds use their embedded revision too; there is no `--to` option.
- `skali compile` prints the compiled definition, including defaults and
  normalized units. Version-selection context goes to stderr.
- `skali skill read manifest`, `skali skill read cli` and `skali skill read
  architecture` serve references embedded in the matching CLI. Bare `skali skill
  read` lists topics; `skali skill read manifest --since <revision>` lists changes.
  Every response identifies the answering release, target, source and mode.

Offline validation does not verify the cluster's currently running release.
Failed reference dispatch never permits substituting another release's documentation.

`skali completion install` puts shell completions in place for the login
shell (`install.sh` already does this); values such as environments, remotes,
run ids, and manifest commands then complete on tab.

`skali remote list` displays incomplete entries and `skali remote remove NAME`
can remove them. Invalid YAML diagnostics identify the configuration file to edit.

## Deployments, values, and stored revisions

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

## Logs and runs

`skali logs [service]` streams live runtime output from an environment; without
a service it merges application logs. `skali run list` lists the environment's
journal. `skali run show <run-id>` prints an operation's steps;
`skali run logs <run-id> --step <step-key>` reads that step's recorded output
(for example `artifacts.web.build`), not the live application's log.
`skali run attach <run-id>` watches a run until it settles, and `skali run
cancel <run-id>` cancels after confirmation. Attaching supports `d` to leave
the run executing and two Ctrl-C presses to cancel. Run IDs and deployment
revision IDs are not interchangeable.

## Remotes, CLI installation, and completions

`skali remote add <name> <host-or-url>` registers and logs in; `remote use
[name]` selects the default for unbound checkouts, and `remote login [name]`
refreshes authentication. Browser login is the default on a terminal;
`--no-browser`, `SKALI_NO_BROWSER=1`, or `--email` selects terminal login.
`remote status` reports reachability and the session. `remote logout [name]`
revokes/forgets credentials but keeps the remote, whereas `remote remove
<name>` removes it. `remote token` prints the stored bearer token to stdout
for API scripting; treat that output as a secret.

`skali upgrade` updates the CLI, managed installed skills, and managed shell
completions. `--channel stable|beta` selects a channel; `--version` selects an
exact tag and cannot be combined with `--channel`. A prerelease CLI defaults
to beta; otherwise stable. This does not upgrade a local or managed cluster.
`skali version` reports the invoked binary and recorded remotes/cache state.

`skali skill install --agent codex`, `--agent claude`, or `--all` installs the
version-neutral guide. Agent selection is required without an interactive
terminal; `--agent` is repeatable and cannot be combined with `--all`. Managed
directories are refreshed/pruned; user-owned skills are refused.
`skali completion <shell>` prints a script; `completion install --shell <shell>`
installs it for bash, zsh, fish, or PowerShell. The default is the login shell
(`SKALI_SHELL` overrides detection). Dynamic completion does not download CLIs.

## Cluster installation and maintenance

Host commands run on the managed Linux node (usually with sudo); on macOS they
operate inside the selected Lima VM. Bare `skali cluster` opens the maintenance
menu. `cluster install` prepares a node, `cluster create` creates a reconciled
seed/coordinator, and `cluster init` initializes the platform. `--config` reads
node/init configuration respectively; `--layout` asserts intended membership.
These are distinct documents with their own schemas, not project manifests:
`skali-node.schema.json`, `skali-init.schema.json`, `skali-layout.schema.json`,
under the same release-specific schema directory as `skali.schema.json`.

`cluster token` issues a join invitation; `token list` and `token revoke`
inspect/revoke invitations. `cluster join` accepts `--token`, `--token-file`
(`-` for stdin), or `SKALI_JOIN_TOKEN`, and can resume saved enrollment.
The two flags are mutually exclusive; either overrides the environment variable.
`cluster node capabilities`,
`node remove`, and `node restore` stage topology changes; `cluster plan`
reviews them, `cluster apply --wait` converges them. `cluster changes import`
replaces the candidate from an already-enrolled layout, and `changes discard`
drops the candidate. `node forget` finalizes an unreachable removed host;
it cannot perform cleanup on that host.

`cluster status`, `diagnose`, and `repair` inspect health and recover scoped
problems. `cluster addresses` declares addressing, `rebalance` reconciles
placement, `tier` applies available database availability, and `storage-migrate`
moves the registry to replicated storage. Read their individual entries for
prerequisites and flags. `cluster reset-password` is controller-side account
recovery; `cluster uninstall` removes the selected scope after confirmation.

`cluster upgrade` normally submits a managed update through the authenticated
remote API and needs no local root session. `--wait` observes the accepted
operation; `--version` pins a target, which must not exceed the installed CLI.
`--recover` is the explicit controller-side recovery path. `--image-tar` and
`--hostd-bin` are source-install inputs, not ordinary release selectors.
Environment backups do not back up the control-plane database, installation
record/encryption key, or registry; full installation recovery remains manual.
The hidden `cluster restore` placeholder is not an available recovery command.

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
- `skali dev -d` waits for rollout then leaves container workloads running.
  It refuses applications with dev blocks because host processes cannot
  outlive the CLI; combine
  `--preview -d` for a fully containerized background preview. `dev down`
  stops the project's workloads and retains data, while `dev down --purge`
  destroys that local environment. `dev reset` destroys the entire local
  platform, including other projects. These scopes are different from
  `dev stop`, which stops the platform while retaining its data.
- One fixed `skali-dev` platform uses the selected target's release. An absent
  platform is created; a matching platform is reused. An older release or k3s
  pin fails before changing it and names `skali dev upgrade`, which moves the
  platform forward in place after showing its plan and asking for confirmation
  (`--yes` skips it). Projects, data, credentials, and the development CA are
  kept. An interrupted upgrade blocks the platform until `skali dev upgrade`
  runs again and finishes it. A newer release, a k3s jump of more than one
  minor version, and working-tree/release transitions require `skali dev
  reset`, which discards the data. `skali dev --remote <name>` selects another
  target.
- `skali dev stop` retains data; `skali dev start` restarts it. Stop, reset and
  platform status work without the remote or a CLI download. Status identifies
  the installed release and an unfinished upgrade. Stop distinguishes stopped,
  already stopped, and absent clusters. Confirmed reset can delete the fixed
  cluster even if its installation record is missing, and also removes a
  partial cluster (a leftover helper container, volume or network) that k3d
  cluster list no longer shows. There is no `dev prune`.
- `skali dev run <name>` runs a named command from the manifest
  (`applications.<app>.commands`) on this machine with the application's
  resolved environment; `skali dev run <app> -- <command>...` runs a raw
  command; bare `skali dev run` lists the declared commands. Database and
  bucket addresses are rewritten to the local platform's loopback ports,
  so seeds and migrations just work.
- `skali dev exec [app]` opens a shell inside the running container
  instead; `skali run` manages journal runs, not project commands.
- `skali dev sql [database]` runs SQL against a database of the local
  platform, exactly like `skali sql` (see Databases) without the target
  flags. It needs no running application and no local psql, so it keeps
  working after `skali dev down`.
- When `skali dev` runs on a remote machine and the browser on a laptop,
  `skali dev proxy <host> --ca sha256:…` on the laptop opens the remote
  platform at the same `https://*.localhost` origins. `skali dev expose
  <address>` on the remote machine opens its edge to the proxy, and `skali dev
  status` there prints the exact proxy command with the CA fingerprint.

## Backups

- `skali backup target set --endpoint <url> --bucket <name> --access-key <id>`
  (admin) configures the external S3 location every snapshot goes to;
  `show` and `unset` read and remove it. Nothing is backed up without it.
- `skali backup create [--environment <name>]` takes a manual snapshot of
  every database, bucket, and volume of the environment; the run streams
  like a deploy. Manual snapshots are kept until removed. An environment
  whose manifest declares no database, bucket, or volume has nothing to
  snapshot and is refused (`nothing_to_back_up`); the Studio disables
  the button there.
- `skali backup schedule set --environment <name> --every "<cron>" --keep
  <duration>` turns automatic backups on for one environment (environment
  admin, recent login): skalid snapshots every database, bucket, and volume
  on the five-field cron schedule (UTC) and deletes the snapshots the
  schedule took once they pass the retention, always keeping the newest.
  `skali backup schedule show` prints the setting, `skali backup schedule
  remove` turns it off (existing snapshots stay and stop expiring). The
  runs carry actor `schedule`. Nothing is on until someone sets it.
- `skali backup list` shows every snapshot of the project with its
  environment and origin (`manual` or `scheduled`); `--environment`
  filters. `skali backup restore [<snapshot-id>]` restores into the
  environment the snapshot came from (or `--environment <other>`) after a
  typed confirmation; the snapshot is verified first, and the environment
  stops while data is written. A restore that fails after the stop keeps
  the environment down and refuses deploys (`restore_incomplete`) until a
  restore completes; `skali backup accept [--environment <name>]` accepts
  the data as it is instead, and the next deploy brings the environment
  back. `skali backup remove <snapshot-id>` deletes a snapshot for good.
  Restore, accept, and remove need `maintain` on the environment and a
  recent login. Snapshots whose original environment was purged need
  project admin; restore requires an explicit `--environment` target. Renamed
  environments keep their snapshots and list them under their current name.

## Buckets

- `skali bucket rotate <key> [--environment <name>] [--retire-after 1h]`
  issues a new S3 keypair for the bucket declared as `buckets.<key>`
  (environment maintain, recent login). The store accepts both keypairs,
  the applications referencing the bucket restart with the new one (the
  run streams like a deploy and succeeds once they all run with it), and
  after the window the previous keypair is retired: requests and presigned
  URLs signed with it fail from then on, so the window must cover the
  longest URL the application issues (`1m` at least for a leaked key, `7d`
  at most). Processes holding the old keypair outside the cluster (`skali
  dev` host runs, a revealed copy) must fetch it again. Rotating again
  inside the window retires the older keypair at once; a bucket a restore
  is rewriting cannot be rotated until the restore finishes.

## Databases

- `skali database rotate <key> [--environment <name>] [--retire-after 1h]`
  issues a new login role for the database declared as `databases.<key>`
  (environment maintain, recent login). The pool accepts both roles, the
  applications referencing the database restart with the new username and
  password (the run streams like a deploy and succeeds once they all run
  with them), and after the window the previous login role is retired: its
  open sessions are terminated and the role is dropped (`1m` at least for
  a leaked password, `7d` at most). The database and everything in it stay
  owned by the owner role the login roles act as, so migrations and
  restores are unaffected; the username changes with every rotation.
  Processes holding the old credentials outside the cluster (`skali dev`
  host runs, a revealed copy) must fetch them again. Rotating again inside
  the window retires the older login role at once.
- `skali database list`, `show <pool>` and `set <pool>` inspect and tune
  the database pools (admin only). The connections column is the pool's
  connection check: `refusing` means new application logins fail for lack
  of connection slots, and `show` names the open incident and which
  databases hold the connections.
  `set --memory` uses Kubernetes quantities such as `4Gi` or `512Mi` and
  cannot be combined with `--auto-memory`. `--set key=value` and `--unset key`
  are repeatable; changes can restart pool instances. These are pool names,
  whereas `database rotate` and `sql` take manifest database keys.
- `skali sql [database] [--environment <name>]` runs SQL against
  `databases.<database>` (the manifest's sole database when omitted) with
  that database's own credentials, in a short-lived client the platform
  starts for the session (a few seconds); no application has to run and
  nothing is installed locally. In a terminal without `-c` or `-f` it is
  an interactive psql. `-c '<sql>'`, `-f <file>` (`-` for stdin) or piped
  stdin run a script: results on stdout, errors and notices on stderr,
  and psql's exit statuses in every format (0 success, 1 client failure,
  2 database unreachable, 3 the script failed; it stops at the first
  error). `--format psql` (default) understands meta-commands such as
  `\d <table>`; `--format csv` drops command tags; `--format json` prints
  one document `{"results": [{"columns": [{"name", "type", "type_oid"}],
  "rows": [[...]], "command", "rows_affected"}], "error": {"code",
  "message", ...}}` with rows as arrays aligned with the columns (COPY TO
  STDOUT output as base64 `copy_data`) and runs the script as one query (one
  implicit transaction, no meta-commands, no COPY FROM STDIN).
  `--read-only` guards against accidental writes (not a permission),
  `--statement-timeout 30s` cancels slow statements on the server, and
  `--timeout 5m` ends the whole session with status 124. Needs `maintain`
  on the environment, plus a recent login on a promote-only one.
  `--command/-c` and `--file/-f` are mutually exclusive; `--format` requires
  a script via one of them or piped stdin, rather than an interactive session.
- `skali exec [app] -- <command>...` runs a command in a running
  application container (an interactive shell without one). Without a
  terminal it pipes stdin, stdout and stderr and exits with the remote
  status; `--no-stdin` sends no input, `--timeout` ends the session with
  status 124, and an interrupted session exits 130. Neither exec nor sql
  ever reads a piped stdin to confirm a login: when a promote-only
  environment needs a recent login, confirm in a terminal first
  (`skali remote login`).
  `--pod` selects a specific pod, `--container` a container in it. TTY allocation
  is automatic when stdin/stdout are terminals; `--tty` forces it, `--no-tty`
  disables it, and `--no-stdin` implies no TTY and cannot be combined with
  `--tty`. Put command flags after `--` so Skali does not parse them.

## Routes

- `skali route list` shows each route of the environment, and each of its
  aliases on a line of its own, with its URL,
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
- `skali env list` lists environments with their state (active, stopped,
  removing), your access, priority, deploy policy, ceiling, and backup
  schedule; `skali env create <name>`, `skali env set --environment <name>
  --max-role read --deploy-policy promote-only`, `skali env stop <name>` (takes
  the workloads down, keeps the data; the next deploy starts it again), and
  `skali env remove <name>` (purge: deletes the environment with all its data,
  typed confirmation) configure them. Stop and remove follow their run until
  done; `--detach` returns right away. Writes need a recent login; the CLI asks
  for the password when it has aged.
- `skali project remove [project]` removes a project with every environment
  and all their data (project admin, typed confirmation, recent login): each
  environment is purged and the project goes with the last of them. It
  reports each environment as it goes; `--detach` returns right away, and
  repeating the command resumes an interrupted removal.
- `skali env rename <environment> <new-name>` renames without redeploying or
  moving data (environment admin, recent login). Promotion rules follow the new
  name; old checkout bindings remain valid through reserved name aliases until
  the environment is purged. `--project` and `--remote` select another project
  or installation. Studio exposes Rename in environment settings.
- A deploy needs `deploy` on the environment for an unchanged definition and
  `maintain` to change the definition or stage values; refusals name the
  required role. A promote-only environment refuses direct deploys and
  names the way in: `skali deploy --from <source> --environment <name>`
  promotes a source's active revision (sources limited by `--promote-from`).
  Environment admins may force a direct deploy with
  `skali deploy --bypass-protection`; it asks for the password when the
  login has aged and the run records the bypass.

<!-- BEGIN GENERATED COMMAND CATALOG -->
## Complete command and flag reference

This catalog is generated from this release's command definitions. Each entry
includes its synopsis, aliases, help, and every public local or inherited flag.
An empty string (`""`) means no explicit value; the description explains any
runtime default. `stringSlice` accepts comma-separated values or repeated flags;
`stringArray` takes one value per occurrence (repeat the flag for more). Boolean
flags can be written alone for true or as `--flag=false`. `duration` flags use
Go duration syntax (for example `1h30m`); string flags such as `--keep` and
`--retire-after` use the documented Skali units, which also include `d` and `w`.
Only the flags listed for a command apply to it; `--remote`, `--environment`,
`--manifest`, `--offline`, and `--yes` are not universal flags. `--` ends Skali
flag parsing before an exec or host command's arguments.

### Command index

- [skali](#skali): Deploy apps and manage Skali clusters
- [skali access](#skali-access): Manage who may do what on a project
- [skali access list](#skali-access-list): Show members and their effective role on every environment
- [skali access remove](#skali-access-remove): Remove a member, or drop their explicit role on one environment
- [skali access set](#skali-access-set): Set a member's project role, or their explicit role on one environment
- [skali backup](#skali-backup): Back up and restore environment data
- [skali backup accept](#skali-backup-accept): Accept the data an incomplete restore left, so the environment can deploy again
- [skali backup create](#skali-backup-create): Snapshot the environment's data to the backup target
- [skali backup list](#skali-backup-list): List the project's snapshots on the backup target
- [skali backup remove](#skali-backup-remove): Delete a snapshot from the backup target
- [skali backup restore](#skali-backup-restore): Restore a snapshot's data into an environment
- [skali backup schedule](#skali-backup-schedule): Configure automatic backups of an environment
- [skali backup schedule remove](#skali-backup-schedule-remove): Turn automatic backups of an environment off
- [skali backup schedule set](#skali-backup-schedule-set): Turn automatic backups on or change their schedule
- [skali backup schedule show](#skali-backup-schedule-show): Show the automatic backup schedule of an environment
- [skali backup target](#skali-backup-target): Manage the external S3 backup target
- [skali backup target set](#skali-backup-target-set): Configure where backups are written
- [skali backup target show](#skali-backup-target-show): Show the configured backup target
- [skali backup target unset](#skali-backup-target-unset): Remove the configured backup target
- [skali bucket](#skali-bucket): Operate the environment's managed buckets
- [skali bucket rotate](#skali-bucket-rotate): Issue a new S3 keypair for a bucket and retire the current one
- [skali cluster](#skali-cluster): Install, maintain, and recover a Skali cluster
- [skali cluster addresses](#skali-cluster-addresses): Declare how this host is addressed inside and outside the cluster
- [skali cluster apply](#skali-cluster-apply): Apply every pending cluster topology change
- [skali cluster changes](#skali-cluster-changes): Manage the candidate cluster revision
- [skali cluster changes discard](#skali-cluster-changes-discard): Discard every unapplied candidate change
- [skali cluster changes import](#skali-cluster-changes-import): Replace the candidate using already-enrolled nodes from a layout
- [skali cluster create](#skali-cluster-create): Create a reconciled cluster seed and coordinator
- [skali cluster diagnose](#skali-cluster-diagnose): Diagnose this installation from host state and the Kubernetes API
- [skali cluster init](#skali-cluster-init): Initialize Skali on the joined cluster (run once on a server)
- [skali cluster install](#skali-cluster-install): Install k3s and prepare this host as a Skali node
- [skali cluster join](#skali-cluster-join): Join this host to an existing cluster
- [skali cluster node](#skali-cluster-node): Stage reconciled-cluster node changes
- [skali cluster node capabilities](#skali-cluster-node-capabilities): Stage a node capability set
- [skali cluster node forget](#skali-cluster-node-forget): Finalize an unreachable removed host without local cleanup
- [skali cluster node remove](#skali-cluster-node-remove): Stage a managed node for drain and removal
- [skali cluster node restore](#skali-cluster-node-restore): Cancel a staged node removal
- [skali cluster plan](#skali-cluster-plan): Preview the candidate cluster transition
- [skali cluster rebalance](#skali-cluster-rebalance): Reconcile the current topology and optionally redistribute workloads
- [skali cluster repair](#skali-cluster-repair): Diagnose and repair this installation with scoped, confirmed actions
- [skali cluster reset-password](#skali-cluster-reset-password): Reset a user's password from a server node
- [skali cluster status](#skali-cluster-status): Show the installation state and health of this host
- [skali cluster storage-migrate](#skali-cluster-storage-migrate): Move the registry volume onto the replicated storage class
- [skali cluster tier](#skali-cluster-tier): Apply the available database availability tier
- [skali cluster token](#skali-cluster-token): Print the join command for this cluster
- [skali cluster token list](#skali-cluster-token-list): List reconciled-cluster invitations
- [skali cluster token revoke](#skali-cluster-token-revoke): Revoke an unused reconciled-cluster invitation
- [skali cluster uninstall](#skali-cluster-uninstall): Remove the Skali bundle or this node, with scoped confirmation
- [skali cluster upgrade](#skali-cluster-upgrade): Update the whole managed cluster to one Skali release
- [skali compile](#skali-compile): Compile a Skali manifest into canonical IR, a revision, or Kubernetes objects
- [skali completion](#skali-completion): Print or install shell completions
- [skali completion bash](#skali-completion-bash): Print the bash completion script
- [skali completion fish](#skali-completion-fish): Print the fish completion script
- [skali completion install](#skali-completion-install): Install the completion script where your shell loads it
- [skali completion powershell](#skali-completion-powershell): Print the powershell completion script
- [skali completion zsh](#skali-completion-zsh): Print the zsh completion script
- [skali database](#skali-database): Operate the managed databases and their pools
- [skali database list](#skali-database-list): List the database pools with their budgets
- [skali database rotate](#skali-database-rotate): Issue new credentials for a database and retire the current ones
- [skali database set](#skali-database-set): Change a database pool's memory budget or parameter overrides
- [skali database show](#skali-database-show): Show a database pool's budget and effective parameters
- [skali deploy](#skali-deploy): Deploy the project to an environment
- [skali dev](#skali-dev): Run the project on the local Skali platform
- [skali dev down](#skali-dev-down): Remove the project from the local platform; data is retained
- [skali dev exec](#skali-dev-exec): Run a command in a running app container
- [skali dev expose](#skali-dev-expose): Let skali dev proxy on another machine reach this machine's local platform
- [skali dev expose stop](#skali-dev-expose-stop): Stop exposing the local platform
- [skali dev list](#skali-dev-list): List projects on the local platform
- [skali dev logs](#skali-dev-logs): Stream the local project's runtime logs
- [skali dev proxy](#skali-dev-proxy): Open a skali dev platform on another machine at its *.localhost origins
- [skali dev proxy stop](#skali-dev-proxy-stop): Stop the proxy running in the background
- [skali dev reset](#skali-dev-reset): Destroy the local dev platform completely
- [skali dev run](#skali-dev-run): Run a command on the host with an application's resolved environment
- [skali dev sql](#skali-dev-sql): Run SQL against a database of the local platform
- [skali dev start](#skali-dev-start): Start the local platform, creating it when absent
- [skali dev status](#skali-dev-status): Show platform and current-project state
- [skali dev stop](#skali-dev-stop): Stop the running local platform; state is retained
- [skali dev trust](#skali-dev-trust): Trust a skali dev platform's development CA in this machine's browsers
- [skali dev up](#skali-dev-up): Build, deploy, and attach to the current project (same as bare skali dev)
- [skali dev upgrade](#skali-dev-upgrade): Upgrade the local platform in place, keeping its data and development CA
- [skali dev values](#skali-dev-values): List the local project's stored values by name and version
- [skali env](#skali-env): List and configure environments
- [skali env create](#skali-env-create): Create an environment
- [skali env list](#skali-env-list): List the project's environments with your access and their settings
- [skali env remove](#skali-env-remove): Remove an environment with its workloads, volumes, values, revisions, and history
- [skali env rename](#skali-env-rename): Rename an environment without changing its data or identity
- [skali env set](#skali-env-set): Change an environment's ceiling, deploy policy, or priority
- [skali env stop](#skali-env-stop): Stop an environment's workloads but keep its data
- [skali exec](#skali-exec): Run a command in a running app container
- [skali help](#skali-help): Help about any command
- [skali logs](#skali-logs): Stream live application logs
- [skali manifest](#skali-manifest): Work on the project manifest
- [skali manifest upgrade](#skali-manifest-upgrade): Clean up obsolete fields and update local manifest review history
- [skali plan](#skali-plan): Preview the deployment plan without building or rolling out workloads
- [skali project](#skali-project): Manage projects
- [skali project remove](#skali-project-remove): Remove a project with every environment and all its data
- [skali remote](#skali-remote): Manage the skali masters this machine talks to
- [skali remote add](#skali-remote-add): Add a remote and log in to it
- [skali remote list](#skali-remote-list): List remotes
- [skali remote login](#skali-remote-login): Log in again (browser, or email and password with --no-browser)
- [skali remote logout](#skali-remote-logout): Revoke a remote's session and forget its token
- [skali remote remove](#skali-remote-remove): Remove a remote and revoke its session
- [skali remote status](#skali-remote-status): Show the current remote, master reachability, session, and user
- [skali remote token](#skali-remote-token): Print the stored session token (for scripting API requests)
- [skali remote use](#skali-remote-use): Switch the current remote
- [skali rollback](#skali-rollback): Roll the environment back to a previous revision
- [skali route](#skali-route): List the environment's routes and probe their domains
- [skali route list](#skali-route-list): List the environment's routes with their certificate and edge verdict
- [skali route probe](#skali-route-probe): Check right now whether the route domains reach this installation
- [skali run](#skali-run): List, inspect, attach to, or cancel runs
- [skali run attach](#skali-run-attach): Attach the terminal to a run until it settles
- [skali run cancel](#skali-run-cancel): Cancel a run
- [skali run list](#skali-run-list): List the environment's runs
- [skali run logs](#skali-run-logs): Print the logs of one step
- [skali run show](#skali-run-show): Print a run's step tree
- [skali skill](#skali-skill): Manage the skali skill for coding agents
- [skali skill install](#skali-skill-install): Install the skali skill for coding agents
- [skali skill read](#skali-skill-read): Print a skali reference at the target's release
- [skali sql](#skali-sql): Run SQL against a database
- [skali upgrade](#skali-upgrade): Update this skali CLI to a published release
- [skali validate](#skali-validate): Validate a Skali project manifest and optionally an environment file
- [skali values](#skali-values): List the environment's stored values by name and version
- [skali version](#skali-version): Print the CLI version

### `skali`

```text
skali [flags]
```

Deploy apps and manage Skali clusters

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for skali |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics |
| `--version`, `-v` | `bool` | `false` | version for skali |

### `skali access`

```text
skali access [flags]
```

Project access is one role ladder, none < read < deploy < maintain < admin,
used at two levels: a member's project role applies to every environment
unless an explicit per-environment role (set with --environment) overrides
it, and an environment's ceiling (skali env set --max-role) caps inherited
roles. Listing needs project read; changes need project admin (environment
admin for --environment) and a recent login.

The project is the checkout's (its binding or manifest) unless --project
names one.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for access |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali access list`

```text
skali access list [flags]
```

Aliases: `ls`.

Show members and their effective role on every environment

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali access remove`

```text
skali access remove <user> [flags]
```

Aliases: `rm`.

Without --environment removes the membership; the user's explicit
per-environment roles go with it and the project disappears for them.
With --environment drops only the explicit role there, so their project
role applies again.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | drop only the explicit role on this environment |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali access set`

```text
skali access set <user> <role> [flags]
```

Without --environment sets the user's project role (read, deploy, maintain,
admin), adding them as a member when needed. With --environment sets an
explicit role on that environment only (none locks it for them); the user
must already be a member. <user> is an email address or a user id.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | set the role on this environment only |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali backup`

```text
skali backup [flags]
```

Back up and restore environment data

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for backup |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali backup accept`

```text
skali backup accept [flags]
```

A restore that stops an environment and does not complete holds it:
the environment stays down and deployments are refused, because its
data may be partial. Re-running a restore (skali backup restore) is
the way back to complete data. When that is not possible, accept
the data as it is: components the restore rewrote may hold partial
data, and the others still hold what they held before.

Accepting lifts the hold only; the environment stays down until the
next deploy. The restore is shown and confirmed by typing the
environment name; --yes skips that.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment the restore holds; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for accept |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the typed confirmation |

### `skali backup create`

```text
skali backup create [flags]
```

Backs up every database, bucket, and application volume of the
environment to the configured S3 target as one complete snapshot.
Configuration and secret values are not included: a snapshot
restores data into a redeployed environment.

The resolved remote, project, and environment are shown and
confirmed before the snapshot starts; --yes skips the question.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the backup and return without following it |
| `--environment` | `string` | `""` | environment to back up; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for create |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali backup list`

```text
skali backup list [flags]
```

Aliases: `ls`.

Lists the snapshots of every environment of the project, newest
first, as the backup target holds them; --environment narrows the
list to one environment. Snapshots of environments that no longer
exist are listed too and can be restored into another environment.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | only list snapshots of this environment |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--project` | `string` | `""` | project whose snapshots to list; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali backup remove`

```text
skali backup remove <snapshot-id> [flags]
```

Aliases: `rm`.

Removes one snapshot of the project from the backup target: its
manifest first, then every database dump, bucket copy, and volume
archive it holds. This cannot be undone. Manual snapshots are only
ever removed this way; snapshots an environment's schedule took also
expire under that environment's retention. A snapshot a restore is
currently reading is refused.

The resolved remote, project, and snapshot are shown and confirmed
before anything is deleted; --yes skips the question. Needs maintain
on the environment the snapshot was taken from and a recent login.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--project` | `string` | `""` | project holding the snapshot; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali backup restore`

```text
skali backup restore [snapshot-id] [flags]
```

Verifies the snapshot's data, stops the environment, replaces every
matching database, bucket, and volume with the snapshot's data, then
resumes the current revision. Current data is overwritten. The
environment must be deployed first; restore moves data, not
configuration. A restore that stops the environment and does not
complete keeps it down and refuses deploys until a restore
completes, or until its data is accepted (skali backup accept).

Without a snapshot id the project's snapshots are offered to pick
from. The snapshot restores into the environment it was taken from
unless --environment names another environment of the same project.
The resolved remote, project, snapshot, and environment are shown
and confirmed by typing the environment name; --yes skips that.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to restore into; defaults to the environment the snapshot was taken from |
| `--help`, `-h` | `bool` | `false` | help for restore |
| `--project` | `string` | `""` | project holding the snapshot; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the typed confirmation |

### `skali backup schedule`

```text
skali backup schedule [flags]
```

Automatic backups are an environment setting: every database, bucket,
and volume of the environment is snapshotted on a cron schedule (UTC),
and snapshots the schedule takes are deleted once older than the
retention (the newest is always kept). Manual snapshots never expire.
Changing the schedule needs environment admin and a recent login.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for schedule |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali backup schedule remove`

```text
skali backup schedule remove [flags]
```

Aliases: `rm`.

Turns automatic backups of the bound environment (or --environment)
off. Snapshots already taken stay on the backup target and no longer
expire; manual backups keep working.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to change; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali backup schedule set`

```text
skali backup schedule set [flags]
```

Sets the automatic backup schedule of the bound environment (or
--environment): --every is a five-field cron expression evaluated in
UTC, --keep how long the snapshots it takes are kept (7d, 4w, 12h; at
least one minute). The first snapshot runs at the next fire of the
schedule, never right away.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to schedule; defaults to the checkout binding |
| `--every` | `string` | `""` | five-field cron expression in UTC, for example "0 3 * * *" **Required.** |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--keep` | `string` | `""` | how long snapshots the schedule takes are kept, for example 7d or 4w **Required.** |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali backup schedule show`

```text
skali backup schedule show [flags]
```

Prints whether automatic backups of the bound environment (or
--environment) are on, their schedule, retention, and next fire.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to show; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for show |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali backup target`

```text
skali backup target [flags]
```

Manage the external S3 backup target

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for target |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali backup target set`

```text
skali backup target set [flags]
```

Stores the external S3 location backups are written to. Admin only;
the secret access key is write-only and never shown again. Without
--secret-key the key is prompted (or read from
SKALI_BACKUP_SECRET_KEY).

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--access-key` | `string` | `""` | S3 access key id **Required.** |
| `--bucket` | `string` | `""` | bucket backups are written into **Required.** |
| `--endpoint` | `string` | `""` | S3 endpoint URL, e.g. https://s3.example.com **Required.** |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--prefix` | `string` | `""` | key prefix inside the bucket |
| `--region` | `string` | `""` | S3 region; empty for providers that ignore it |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--secret-key` | `string` | `""` | S3 secret access key (prefer the prompt or SKALI_BACKUP_SECRET_KEY) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali backup target show`

```text
skali backup target show [flags]
```

Show the configured backup target

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for show |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali backup target unset`

```text
skali backup target unset [flags]
```

Removes the backup target and its stored credentials. Admin only.
Snapshots already written to the bucket are untouched; setting the
same target again makes them listable and restorable once more.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for unset |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali bucket`

```text
skali bucket [flags]
```

Managed object storage buckets of an environment. Buckets are declared
in skali.yml; this group holds the operations on a provisioned bucket
that are not configuration.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for bucket |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali bucket rotate`

```text
skali bucket rotate <bucket> [flags]
```

Issues a new keypair for the bucket named by its manifest key. The
store accepts both keypairs, the applications referencing the bucket
restart with the new one, and after the overlap window the previous
keypair is retired for good: requests and presigned URLs signed with
it fail from that instant on, so --retire-after must cover the longest
URL the application issues (1h by default, 1m for a key known to be
leaked, 7d at most). Processes holding the old keypair outside the
cluster (skali dev host runs, anyone who revealed it) must fetch it
again. Rotating again inside the window retires the older keypair at
once. A bucket a restore is rewriting cannot be rotated until the
restore has finished.

The run streams like a deploy and succeeds once every consumer runs
with the new keypair; the previous keypair retires on schedule either
way. Needs maintain on the environment and a recent login.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the rotation and return without following it |
| `--environment` | `string` | `""` | environment holding the bucket; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for rotate |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--retire-after` | `string` | `1h` | how long the previous keypair stays accepted, such as 30m, 1h, or 2d (at least 1m, at most 7d) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali cluster`

```text
skali cluster [flags]
```

Install, maintain, and recover a Skali cluster

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for cluster |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) |

### `skali cluster addresses`

```text
skali cluster addresses [flags]
```

Declare how this host is addressed inside and outside the cluster

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--cluster-ip` | `string` | `""` | address other cluster nodes reach this node through |
| `--coordinator-bind` | `stringSlice` | `[]` | scopes the enrollment coordinator listens on: cluster, public, or both |
| `--help`, `-h` | `bool` | `false` | help for addresses |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--public-ip` | `stringSlice` | `[]` | address reachable from outside the cluster network; repeatable |
| `--tls-san` | `stringSlice` | `[]` | additional name or address for the kubernetes api certificate; repeatable |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--yes` | `bool` | `false` | record the declaration without confirmation |

### `skali cluster apply`

```text
skali cluster apply [flags]
```

Apply every pending cluster topology change

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for apply |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--rebalance-workloads` | `bool` | `false` | also redistribute eligible healthy managed workloads |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--wait` | `bool` | `false` | wait until the target revision converges |
| `--yes` | `bool` | `false` | apply without an interactive confirmation |

### `skali cluster changes`

```text
skali cluster changes [flags]
```

Manage the candidate cluster revision

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for changes |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster changes discard`

```text
skali cluster changes discard [flags]
```

Discard every unapplied candidate change

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for discard |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster changes import`

```text
skali cluster changes import <cluster-layout> [flags]
```

Replace the candidate using already-enrolled nodes from a layout

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for import |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster create`

```text
skali cluster create [flags]
```

Create a reconciled cluster seed and coordinator

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--config` | `string` | `""` | non-interactive node configuration (node.yaml) |
| `--help`, `-h` | `bool` | `false` | help for create |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--yes` | `bool` | `false` | provision missing Mac dependencies without confirmation (macOS only) |

### `skali cluster diagnose`

```text
skali cluster diagnose [flags]
```

Diagnose this installation from host state and the Kubernetes API

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for diagnose |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster init`

```text
skali cluster init [flags]
```

Initialize Skali on the joined cluster (run once on a server)

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--config` | `string` | `""` | non-interactive init configuration (init.yaml) |
| `--help`, `-h` | `bool` | `false` | help for init |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--layout` | `string` | `""` | cluster-layout document asserting the expected membership |
| `--platform-preference` | `string` | `""` | ordered build platform preference for mixed-architecture clusters, comma-separated (for example linux/arm64); empty keeps the recorded choice |
| `--storage-driver` | `string` | `""` | application storage driver: local (default) or longhorn; re-run init with longhorn to enable it on an existing cluster |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster install`

```text
skali cluster install [flags]
```

Install k3s and prepare this host as a Skali node

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--config` | `string` | `""` | non-interactive node configuration (node.yaml) |
| `--help`, `-h` | `bool` | `false` | help for install |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--yes` | `bool` | `false` | provision missing Mac dependencies without confirmation (macOS only) |

### `skali cluster join`

```text
skali cluster join [coordinator] [flags]
```

Join this host to an existing cluster

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--capabilities` | `stringSlice` | `[]` | designated workload capabilities for this node |
| `--cluster` | `string` | `""` | expected cluster name; required only for raw legacy K10 tokens |
| `--coordinator-bind` | `stringSlice` | `[]` | scopes the enrollment coordinator listens on: cluster, public, or both (servers only) |
| `--help`, `-h` | `bool` | `false` | help for join |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--node-ip` | `string` | `""` | address other cluster nodes reach this node through; defaults to the coordinator route source when unambiguous |
| `--public-ip` | `stringSlice` | `[]` | address reachable from outside the cluster network; repeatable |
| `--role` | `string` | `""` | expected role; must match the invitation or legacy token |
| `--server` | `string` | `""` | coordinator endpoint; legacy composite tokens may supply their k3s endpoint |
| `--tls-san` | `stringSlice` | `[]` | additional name or address for the kubernetes api certificate; repeatable |
| `--token` | `string` | `""` | join token (defaults to SKALI_JOIN_TOKEN or saved enrollment) |
| `--token-file` | `string` | `""` | path to a join token file, or - for stdin |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--yes` | `bool` | `false` | provision missing Mac dependencies without confirmation (macOS only) |

### `skali cluster node`

```text
skali cluster node [flags]
```

Stage reconciled-cluster node changes

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for node |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster node capabilities`

```text
skali cluster node capabilities <node> <capability>... [flags]
```

Stage a node capability set

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for capabilities |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster node forget`

```text
skali cluster node forget <node> [flags]
```

Finalize an unreachable removed host without local cleanup

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--force` | `bool` | `false` | accept possible stale k3s and agent state on the unreachable host |
| `--help`, `-h` | `bool` | `false` | help for forget |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster node remove`

```text
skali cluster node remove <node> [flags]
```

Aliases: `rm`.

Stage a managed node for drain and removal

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster node restore`

```text
skali cluster node restore <node> [flags]
```

Cancel a staged node removal

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for restore |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster plan`

```text
skali cluster plan [flags]
```

Preview the candidate cluster transition

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for plan |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--rebalance-workloads` | `bool` | `false` | include controlled rolling redistribution of healthy managed workloads |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster rebalance`

```text
skali cluster rebalance [flags]
```

Reconcile the current topology and optionally redistribute workloads

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for rebalance |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--wait` | `bool` | `false` | wait for rebalancing to complete |
| `--workloads` | `bool` | `false` | roll eligible managed workloads to use the current topology |
| `--yes` | `bool` | `false` | rebalance without an interactive confirmation |

### `skali cluster repair`

```text
skali cluster repair [flags]
```

Diagnose and repair this installation with scoped, confirmed actions

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for repair |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--yes` | `bool` | `false` | confirm every planned repair action |

### `skali cluster reset-password`

```text
skali cluster reset-password [flags]
```

Reset a user's password without knowing the old one. This is the recovery
path when nobody can sign in anymore: it runs on a server node with root
access and needs no working login. Every session of the user is revoked;
--disable-2fa also removes a lost authenticator.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--disable-2fa` | `bool` | `false` | also remove the user's two-factor enrollment |
| `--email` | `string` | `""` | login email of the user (prompted when omitted) |
| `--help`, `-h` | `bool` | `false` | help for reset-password |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--password-stdin` | `bool` | `false` | read the new password from stdin instead of prompting |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster status`

```text
skali cluster status [flags]
```

Show the installation state and health of this host

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--all` | `bool` | `false` | include cancelled and removed node history |
| `--help`, `-h` | `bool` | `false` | help for status |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster storage-migrate`

```text
skali cluster storage-migrate [flags]
```

Move the registry volume onto the replicated storage class

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for storage-migrate |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation prompt |

### `skali cluster tier`

```text
skali cluster tier [flags]
```

Apply the available database availability tier

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for tier |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation prompt |

### `skali cluster token`

```text
skali cluster token [flags]
```

Print the join command for this cluster

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--capabilities` | `stringSlice` | `[]` | optional capabilities this invitation allows (default: all) |
| `--help`, `-h` | `bool` | `false` | help for token |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--role` | `string` | `agent` | role the token enrolls: agent or server (interactive runs ask) |
| `--server` | `string` | `""` | advertised coordinator endpoint override (k3s endpoint for legacy clusters) |
| `--ttl` | `duration` | `24h0m0s` | invitation lifetime for reconciled clusters |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster token list`

```text
skali cluster token list [flags]
```

Aliases: `ls`.

List reconciled-cluster invitations

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster token revoke`

```text
skali cluster token revoke <id> [flags]
```

Revoke an unused reconciled-cluster invitation

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for revoke |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster uninstall`

```text
skali cluster uninstall [flags]
```

Remove the Skali bundle or this node, with scoped confirmation

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--confirm` | `string` | `""` | cluster name, confirming the removal non-interactively |
| `--help`, `-h` | `bool` | `false` | help for uninstall |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--scope` | `string` | `""` | removal scope: bundle (keep bare k3s) or node (remove k3s and all skali state) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |

### `skali cluster upgrade`

```text
skali cluster upgrade [flags]
```

Update the whole managed cluster to one Skali release

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for upgrade |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--recover` | `bool` | `false` | submit directly from a controller without API activity checks |
| `--remote` | `string` | `""` | target remote, overriding checkout and current selection |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--version` | `string` | `""` | exact target release (default: latest on the configured channel) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--wait` | `bool` | `false` | wait for the cluster update to complete |
| `--yes` | `bool` | `false` | skip the confirmation prompt |

### `skali compile`

```text
skali compile [flags]
```

Compile a Skali manifest into canonical IR, a revision, or Kubernetes objects

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--env-file` | `string` | `""` | dotenv file used to resolve project variables for revision or kubernetes output |
| `--environment` | `string` | `local` | environment name recorded in revision output |
| `--help`, `-h` | `bool` | `false` | help for compile |
| `--image` | `stringArray` | `[]` | prepared image for a build application, as service=reference |
| `--manifest` | `string` | `""` | manifest path; defaults to skali.yml or skali.yaml |
| `--namespace` | `string` | `""` | Kubernetes namespace used for rendering |
| `--offline` | `bool` | `false` | use the recorded target release and an available CLI without contacting the remote or downloading |
| `--remote` | `string` | `""` | target remote, overriding the checkout binding and current remote |
| `--target` | `string` | `definition` | compile target: definition, revision, or kubernetes |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali completion`

```text
skali completion [flags]
```

Shell completions complete skali's commands and flags and, where a
value has a known set, the values too: remotes, environments, projects,
run and snapshot ids, members, the manifest's applications and named
commands, and fixed choices such as roles and channels. Remote-backed
values come from the remote the command line points at and give up
silently after two seconds.

skali completion install writes the script where your shell loads it;
install.sh runs it for your login shell. The shell subcommands print
the script for a manual setup.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for completion |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali completion bash`

```text
skali completion bash [flags]
```

Prints the bash completion script to stdout. Source it from your
shell's startup file, or let skali completion install place it where
the shell loads it on its own.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for bash |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali completion fish`

```text
skali completion fish [flags]
```

Prints the fish completion script to stdout. Source it from your
shell's startup file, or let skali completion install place it where
the shell loads it on its own.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for fish |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali completion install`

```text
skali completion install [flags]
```

Writes the completion script for your login shell (or --shell) into the
directory the shell already scans, so no startup file changes: bash
loads ~/.local/share/bash-completion/completions, fish
~/.config/fish/completions, and zsh a site-functions directory on its
fpath, Homebrew's when it is writable. Without one, the script lands in
~/.local/share/zsh/site-functions and the command prints the fpath line
to add before compinit in ~/.zshrc.

An installed script is refreshed by skali upgrade. New shells pick it
up; running shells need a restart (zsh: rm ~/.zcompdump first if the
new commands do not appear).

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for install |
| `--shell` | `string` | `""` | shell to install for: bash, zsh, or fish (default: your login shell) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali completion powershell`

```text
skali completion powershell [flags]
```

Prints the powershell completion script to stdout. Source it from your
shell's startup file, or let skali completion install place it where
the shell loads it on its own.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for powershell |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali completion zsh`

```text
skali completion zsh [flags]
```

Prints the zsh completion script to stdout. Source it from your
shell's startup file, or let skali completion install place it where
the shell loads it on its own.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for zsh |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali database`

```text
skali database [flags]
```

Managed PostgreSQL databases and the pools they run on. `rotate` acts
on one database of an environment, declared in skali.yml. `list`,
`show` and `set` inspect and tune the pools: every pool runs a
parameter set derived from its memory budget (automatic from the
smallest database node, or set explicitly) with per-parameter
overrides on top; those three are admin only.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for database |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali database list`

```text
skali database list [flags]
```

Aliases: `ls`.

List the database pools with their budgets

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali database rotate`

```text
skali database rotate <database> [flags]
```

Issues a new login role for the database named by its manifest key. The
pool accepts both roles, the applications referencing the database
restart with the new username and password, and after the overlap
window the previous login role is retired for good: its open sessions
are terminated and the role is dropped (1h by default, 1m for a
password known to be leaked, 7d at most). The database and everything
in it stay owned by the owner role the login roles act as, so
migrations and restores are unaffected. Processes holding the old
credentials outside the cluster (skali dev host runs, anyone who
revealed them) must fetch them again. Rotating again inside the window
retires the older login role at once.

The run streams like a deploy and succeeds once every consumer runs
with the new credentials; the previous login role retires on schedule
either way. Needs maintain on the environment and a recent login.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the rotation and return without following it |
| `--environment` | `string` | `""` | environment holding the database; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for rotate |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--retire-after` | `string` | `1h` | how long the previous login role stays accepted, such as 30m, 1h, or 2d (at least 1m, at most 7d) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali database set`

```text
skali database set <name> [flags]
```

Retunes one pool. --memory sets an explicit budget (a Kubernetes
quantity such as 4Gi); --auto-memory returns it to the automatic size.
--set key=value adds or replaces one override in PostgreSQL syntax
(work_mem=64MB); --unset key removes one. Parameters that reload live
take effect without interruption; the ones that need a restart
(shared_buffers, max_connections, wal_buffers, max_worker_processes)
restart the pool's instances, so a single-instance pool is briefly
unavailable. Admin only, sudo mode.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--auto-memory` | `bool` | `false` | size the budget automatically from the smallest database node |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--memory` | `string` | `""` | explicit memory budget, e.g. 4Gi |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--set` | `stringArray` | `[]` | override one parameter, key=value in PostgreSQL syntax (repeatable) |
| `--unset` | `stringArray` | `[]` | remove one override (repeatable) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes`, `-y` | `bool` | `false` | skip the confirmation prompt |

### `skali database show`

```text
skali database show <name> [flags]
```

Show a database pool's budget and effective parameters

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for show |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali deploy`

```text
skali deploy [flags]
```

Deploys the local manifest: candidate definition, staged values, plan
confirmation, local builds and imports pushed to the managed registry,
server-side verification, and the journaled rollout, rendered live.
Values come from --env-file, else the env file (or stored values)
remembered for the environment in .skali/, else a one-time pick among
the checkout's .env/.env.* files that is remembered from then on;
--pick-env-file asks again. Without env files the stored values apply.

Builds and pushes need this terminal, so Ctrl-C during them (or
closing the terminal) cancels the deployment. Once they are done the
run continues on the server and the live view says so: d detaches
and leaves it running, Ctrl-C pressed twice cancels it. --detach
returns as soon as the run is handed over.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--allow-destructive` | `bool` | `false` | approve a destructive plan (non-interactive) |
| `--build` | `string` | `auto` | build executor: local or auto (auto follows installation policy; local today) |
| `--bypass-protection` | `bool` | `false` | deploy into a promote-only environment anyway (environment admins with a recent login; recorded on the run) |
| `--detach` | `bool` | `false` | return after builds and uploads hand the run to the server, without waiting for the rollout |
| `--env-file` | `string` | `""` | dotenv file to stage as candidate values for this run only (default: remembered choice; with --from: stored target values; plan only validates the file) |
| `--environment` | `string` | `""` | target environment name (defaults to the checkout binding, prompted interactively otherwise) |
| `--force` | `bool` | `false` | deploy even when nothing changed; application workloads are restarted (data is untouched) |
| `--from` | `string` | `""` | promote the active revision of another environment of this project; nothing builds and no manifest is read |
| `--help`, `-h` | `bool` | `false` | help for deploy |
| `--manifest` | `string` | `""` | explicit manifest path (otherwise discover skali.yaml or skali.yml up from the working directory) |
| `--pick-env-file` | `bool` | `false` | ask which env file (or the stored values) to use and remember the answer for the environment (not used with --from) |
| `--platform` | `string` | `""` | override the build platform(s), e.g. linux/amd64 or a comma list (default: application platforms intersected with cluster platforms and preference) |
| `--prune-values` | `bool` | `false` | remove stored values the manifest no longer references as part of this deployment |
| `--rebuild` | `bool` | `false` | rebuild and re-import artifacts without caches, picking up moved base images (implies --force) |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | approve a non-destructive plan without prompting |

### `skali dev`

```text
skali dev [flags]
```

Bare skali dev (shorthand for skali dev up) is the complete paved
path: it ensures the disposable local platform (k3d cluster with
in-cluster skalid, Postgres, and registry), builds and deploys the
current project, attaches to the rollout, and follows the runtime
logs. Like docker compose, ending the session (Ctrl-C, closing the
terminal) pauses the project; its data is retained and the next
skali dev brings it back. Pressing d while the logs follow detaches
instead: the session ends and the project keeps running, as if
started with -d. A rollout already in flight is adopted: dev
attaches to it instead of failing; --force cancels it and
redeploys. Use -d for a background project that keeps running,
skali dev down to pause it explicitly, and skali dev list to see
everything on the local platform. The platform runs the same
Skali release as the project's target cluster (the checkout
binding, else the current remote, else this skali), using that release
in one local cluster; skali dev upgrade moves it to a newer release in
place. The platform lifecycle lives under skali dev start, stop,
upgrade, and reset. Values come from the env file remembered for the local
environment in .skali/ (a lone .env/.env.* file is taken as is, several
are picked from once); --pick-env-file asks again. Local values never
leave this machine.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach`, `-d` | `bool` | `false` | exit once the rollout settles instead of following runtime logs |
| `--env-file` | `string` | `""` | explicit local env file for this run only (default: the choice remembered for local, else the lone .env/.env.* file, else a one-time pick) |
| `--force` | `bool` | `false` | deploy even when nothing changed, cancelling any in-flight run first; application workloads are restarted (data is untouched) |
| `--help`, `-h` | `bool` | `false` | help for dev |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI |
| `--pick-env-file` | `bool` | `false` | ask which env file (or the stored values) to use and remember the answer |
| `--platform` | `string` | `""` | override the build platform(s), e.g. linux/amd64 or a comma list (default: the cluster architecture) |
| `--preview` | `bool` | `false` | build and deploy every application in the cluster, ignoring dev blocks; no local dev processes run |
| `--prune-values` | `bool` | `false` | remove stored values the manifest no longer references as part of this deployment |
| `--rebuild` | `bool` | `false` | rebuild and re-import artifacts without caches, picking up moved base images (implies --force) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev down`

```text
skali dev down [flags]
```

Removes the current project's running workloads from the local
platform, like docker compose down: the cluster and every other
project keep running, and this project's volumes, values, and
revision history are retained, so the next skali dev brings it
back with its data. With --purge the project's local environment
is destroyed completely, including volumes and all values,
revisions, and history; that decision is one-way.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for down |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--purge` | `bool` | `false` | destroy the environment completely, including volumes and all data |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation for --purge |

### `skali dev exec`

```text
skali dev exec [service] [flags] -- <command>...
```

Exec opens a command or interactive shell inside a running container of
one service on the local platform, like docker exec. Without a command
it starts /bin/sh.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--container` | `string` | `""` | container name (default: the service's own container) |
| `--help`, `-h` | `bool` | `false` | help for exec |
| `--no-stdin`, `-n` | `bool` | `false` | do not forward stdin: the command sees end of input at once (like ssh -n); implies --no-tty |
| `--no-tty` | `bool` | `false` | never allocate a remote TTY |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--pod` | `string` | `""` | exec into this pod instead of the newest ready one |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--timeout` | `duration` | `0s` | end the session after this long, such as 30s or 5m, and exit with status 124 (default: no limit) |
| `--tty`, `-t` | `bool` | `false` | allocate a remote TTY even when stdout is not a terminal |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev expose`

```text
skali dev expose [address] [flags]
```

Opens this machine's local platform to skali dev proxy on another machine.
The platform's edge only listens on loopback; expose listens on ports 443
and 80 of one address of this machine, such as its LAN or VPN address, and
relays raw TCP to the edge. It prints the skali dev proxy command to run on
the other machine, including the CA fingerprint.

Anyone who reaches that address reaches the development edge: the Studio
and every route of every project on the local platform. Pick an address
only your own machines reach; the wildcard address is refused. The address
is remembered: a bare skali dev expose reuses it. -d runs it in the
background; skali dev expose stop ends it. Tailscale users can run
tailscale serve instead. On macOS, sudo asks for your password to open
the ports, and expose keeps running as you.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach`, `-d` | `bool` | `false` | run in the background; skali dev expose stop ends it |
| `--help`, `-h` | `bool` | `false` | help for expose |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev expose stop`

```text
skali dev expose stop [flags]
```

Stop exposing the local platform

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for stop |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev list`

```text
skali dev list [flags]
```

Aliases: `ls`.

List projects on the local platform

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev logs`

```text
skali dev logs [service] [flags]
```

Stream the local project's runtime logs

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for logs |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev proxy`

```text
skali dev proxy [host] [flags]
```

Makes this machine the entrypoint of a skali dev platform that runs on
another machine, without a cluster or Docker here. The proxy listens on
this machine's ports 443 and 80 and relays raw TCP to the same ports on
host, so https://<domain>.localhost in a browser here is the same origin
it is on the other machine: TLS ends at the remote edge and nothing is
rewritten.

The remote edge only listens on that machine's loopback, so something there
has to pass outside traffic to it: skali dev expose <address>, or tailscale
serve. How this machine reaches host (a LAN, a VPN, a tailnet) is up to you.

Browsers here must trust the remote platform's development CA. The proxy
fetches it from host and accepts it only when it matches --ca, the
fingerprint skali dev status prints on the other machine, then installs it
like skali dev trust. Host and fingerprint are remembered: a bare skali dev
proxy reuses them. -d runs the proxy in the background; skali dev proxy
stop ends it.

macOS lets only administrators listen on ports 443 and 80 of loopback:
sudo asks for your password and opens them, and the proxy keeps running
as you.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--ca` | `string` | `""` | fingerprint of the remote platform's development CA, as skali dev status prints it there (sha256:…); remembered after the first run |
| `--detach`, `-d` | `bool` | `false` | run in the background; skali dev proxy stop ends it |
| `--help`, `-h` | `bool` | `false` | help for proxy |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev proxy stop`

```text
skali dev proxy stop [flags]
```

Stop the proxy running in the background

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for stop |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev reset`

```text
skali dev reset [flags]
```

Deletes the fixed local cluster and its data, even if its installation record is missing,
together with its development CA. Requires confirmation or --yes. Moving to a newer
release does not need it: skali dev upgrade keeps the platform's data and CA.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for reset |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali dev run`

```text
skali dev run [app] [name] -- <command>... [flags]
```

Runs a named command from the manifest (applications.<app>.commands)
or a raw command after --, on this machine in the project root, with
the application's fully resolved environment: stored values plus
database and bucket outputs rewritten to the local platform's
loopback ports. Seeds and migrations against the dev database live
here. Bare skali dev run lists the declared commands, skali dev run
<app> those of one application. For a shell inside the running
container, see skali dev exec; skali run manages journal runs, not
project commands.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for run |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev sql`

```text
skali dev sql [database] [flags]
```

Sql runs SQL against one database of the project on the local platform, declared as
databases.<database> in skali.yml, connected with that database's own
credentials. Nothing needs to be installed locally and no application
has to run: the platform starts a short-lived database client next to
the environment's applications for the session (a few seconds, longer
while its image is first pulled).

In a terminal, without -c or -f, it opens an interactive psql. Otherwise
it runs a script: the -c text, the -f file (- for stdin), or stdin
itself. Results go to stdout, errors and notices to stderr, and the
exit status follows psql: 0 success, 1 a client failure, 2 the
database unreachable, 3 the script failed (a script stops at its
first error).

--format psql (the default) is psql's own output and understands psql
meta-commands such as \d. --format csv prints psql's CSV without command
tags. --format json prints one document for the whole script:

  {"results": [{"columns": [{"name": "id", "type": "int4", "type_oid": 23}],
                "rows": [[1]], "command": "SELECT 1", "rows_affected": 1}],
   "error": {"code": "42P01", "message": "..."}}

Rows are arrays aligned with the columns; booleans, numbers and json
keep their JSON types, everything else is PostgreSQL's text form; the
output of COPY ... TO STDOUT is kept byte for byte as base64 in
"copy_data" ("copy_encoding": "base64"). The json
format runs the script as one query, so its statements share one
implicit transaction unless the script manages its own; psql
meta-commands and COPY ... FROM STDIN are not available there.

--read-only sets default_transaction_read_only, a guard against
accidental writes rather than a permission. The session needs maintain
on the environment, like exec, and a recent login on a promote-only one.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--command`, `-c` | `string` | `""` | run this SQL and exit (several statements may be separated by semicolons) |
| `--file`, `-f` | `string` | `""` | run the SQL script in this file and exit; - reads it from stdin |
| `--format` | `string` | `""` | output of a script: psql (default), csv, or json |
| `--help`, `-h` | `bool` | `false` | help for sql |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--read-only` | `bool` | `false` | start every transaction read-only (a guard, not a permission) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--statement-timeout` | `duration` | `0s` | cancel any statement running longer than this, such as 30s (default: the server's) |
| `--timeout` | `duration` | `0s` | end the session after this long, such as 30s or 5m, and exit with status 124 (default: no limit) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev start`

```text
skali dev start [flags]
```

Boots the local platform: creates it when absent, starts it when
stopped, and leaves a running one untouched beyond a health check.
Project data survives a stop/start cycle. --force runs a full
converge pass (re-applying the complete platform bundle) even when
the platform looks healthy; it is the repair verb for a platform
whose recorded state lies. The platform's release is this skali's;
an older release or k3s pin needs skali dev upgrade first.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--force` | `bool` | `false` | force a full converge pass even when the platform looks healthy |
| `--help`, `-h` | `bool` | `false` | help for start |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev status`

```text
skali dev status [flags]
```

Show platform and current-project state

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for status |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev stop`

```text
skali dev stop [flags]
```

Stop the running local platform; state is retained

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for stop |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev trust`

```text
skali dev trust [host] [flags]
```

The local platform serves every route over https from a development CA
generated for this installation. skali itself trusts it; browsers need it
in the OS trust store. skali dev offers this once after creating the
platform; this command installs it (again) or, with --check, only reports
where it is trusted. Installing is an addition of one certificate and safe
to repeat; macOS asks for the login password, Linux for sudo.

With a host, it trusts the CA of a skali dev platform on another machine
instead, fetched from the edge reachable at that host (port 443). --ca is the
fingerprint skali dev status prints on that machine, and the CA is accepted
only when it matches. Use this when something else already forwards the
remote edge to this machine, such as ssh -L for ports 443 and 80, with
localhost as the host; skali dev proxy trusts the CA by itself.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--ca` | `string` | `""` | with a host: the fingerprint of that platform's development CA, as skali dev status prints it there (sha256:…) |
| `--check` | `bool` | `false` | report the trust state without installing anything |
| `--help`, `-h` | `bool` | `false` | help for trust |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev up`

```text
skali dev up [flags]
```

Build, deploy, and attach to the current project (same as bare skali dev)

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach`, `-d` | `bool` | `false` | exit once the rollout settles instead of following runtime logs |
| `--env-file` | `string` | `""` | explicit local env file for this run only (default: the choice remembered for local, else the lone .env/.env.* file, else a one-time pick) |
| `--force` | `bool` | `false` | deploy even when nothing changed, cancelling any in-flight run first; application workloads are restarted (data is untouched) |
| `--help`, `-h` | `bool` | `false` | help for up |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--pick-env-file` | `bool` | `false` | ask which env file (or the stored values) to use and remember the answer |
| `--platform` | `string` | `""` | override the build platform(s), e.g. linux/amd64 or a comma list (default: the cluster architecture) |
| `--preview` | `bool` | `false` | build and deploy every application in the cluster, ignoring dev blocks; no local dev processes run |
| `--prune-values` | `bool` | `false` | remove stored values the manifest no longer references as part of this deployment |
| `--rebuild` | `bool` | `false` | rebuild and re-import artifacts without caches, picking up moved base images (implies --force) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali dev upgrade`

```text
skali dev upgrade [flags]
```

Moves the existing local platform to this skali's release and k3s pin
without a reset: projects, environments, values, credentials, databases,
buckets, volumes, and the development CA (so its trust and every dev proxy
pinned to it) stay as they are. The plan (versions, component changes,
database migrations, and the expected interruption) is shown before
anything changes and needs confirmation or --yes. Releases only move
forward and k3s one minor version at a time; anything else is refused
before it starts. An interrupted or failed upgrade keeps all data;
running skali dev upgrade again continues it.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for upgrade |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | apply the plan without asking |

### `skali dev values`

```text
skali dev values [flags]
```

List the local project's stored values by name and version

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for values |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali env`

```text
skali env [flags]
```

Environments of the checkout's project (or --project). Listing needs
project read, creating needs project maintain (the creator becomes
admin of the new environment), settings, stop, and removal need environment
admin and a recent login. Automatic backups are configured with
skali backup schedule.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for env |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali env create`

```text
skali env create <name> [flags]
```

Creates an environment in the project; you become its admin. Priority high
(instance admins only) marks an environment that keeps running when
resources are tight; it starts with a read ceiling for inheriting members.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for create |
| `--priority` | `string` | `""` | normal (default) or high (instance admins only) |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali env list`

```text
skali env list [flags]
```

Aliases: `ls`.

List the project's environments with your access and their settings

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali env remove`

```text
skali env remove <name> [flags]
```

Aliases: `rm`.

Purges the environment: the namespace with its volumes, the databases and
buckets with their data, and the environment with all values, revisions, and
history are deleted. This is one-way. The command follows the removal until
the environment is gone; --detach returns right away, and running it again
while the removal is in progress follows that removal instead of restarting
it. Environment admin and a recent login are required.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the removal and return without following it |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the typed confirmation |

### `skali env rename`

```text
skali env rename <environment> <new-name> [flags]
```

Rename an environment without changing its data or identity

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for rename |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali env set`

```text
skali env set [flags]
```

Changes settings of the bound environment (or --environment); only the
flags given change. --max-role caps the roles members inherit from their
project role (explicit per-environment roles and project admins are not
capped). --deploy-policy promote-only refuses direct deploys; --promote-from
names the environments promotions may come from (any to allow every
environment of the project). Raising --priority to high is instance admin
only.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--deploy-policy` | `string` | `""` | direct or promote-only |
| `--environment` | `string` | `""` | environment to change; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--max-role` | `string` | `""` | ceiling for inherited roles: none, read, deploy, maintain, or admin |
| `--priority` | `string` | `""` | normal or high |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--promote-from` | `string` | `""` | comma-separated source environments for promotions, or any |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali env stop`

```text
skali env stop <name> [flags]
```

Takes the running workloads down. The namespace with its volumes, the values,
revisions, and history stay, and the next deploy starts the environment
again. The command follows the stop until it is done; --detach returns
right away, and running it again while the stop is in progress follows that
stop. Environment admin and a recent login are required.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the stop and return without following it |
| `--help`, `-h` | `bool` | `false` | help for stop |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali exec`

```text
skali exec [service] [flags] -- <command>...
```

Exec opens a command or interactive shell inside a running container of
one service, like docker exec. Without a command it starts /bin/sh.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--container` | `string` | `""` | container name (default: the service's own container) |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--help`, `-h` | `bool` | `false` | help for exec |
| `--no-stdin`, `-n` | `bool` | `false` | do not forward stdin: the command sees end of input at once (like ssh -n); implies --no-tty |
| `--no-tty` | `bool` | `false` | never allocate a remote TTY |
| `--pod` | `string` | `""` | exec into this pod instead of the newest ready one |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--timeout` | `duration` | `0s` | end the session after this long, such as 30s or 5m, and exit with status 124 (default: no limit) |
| `--tty`, `-t` | `bool` | `false` | allocate a remote TTY even when stdout is not a terminal |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali help`

```text
skali help [command] [flags]
```

Help provides help for any command in the application.
Simply type skali help [path to command] for full details.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for help |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali logs`

```text
skali logs [service] [flags]
```

Stream live application logs

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--help`, `-h` | `bool` | `false` | help for logs |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali manifest`

```text
skali manifest [flags]
```

Work on the project manifest

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for manifest |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali manifest upgrade`

```text
skali manifest upgrade [flags]
```

Validates safe manifest edits before writing. Relevant semantic changes require explicit review and --acknowledge. Review history is stored in .skali/, never in the manifest.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--acknowledge` | `bool` | `false` | confirm review of relevant semantic changes |
| `--help`, `-h` | `bool` | `false` | help for upgrade |
| `--manifest` | `string` | `""` | manifest path; defaults to skali.yml or skali.yaml |
| `--offline` | `bool` | `false` | use the recorded target release and an available CLI without contacting the remote or downloading |
| `--remote` | `string` | `""` | target remote, overriding the checkout binding and current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali plan`

```text
skali plan [flags]
```

Compiles and submits the candidate definition, then prints the server's
plan without building images, uploading env-file values, or changing the
environment's active revision. Local review and env-file selection may be
recorded. --env-file is validated locally; the plan uses stored values.
With --from, previews promotion of the source's active revision instead.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--bypass-protection` | `bool` | `false` | deploy into a promote-only environment anyway (environment admins with a recent login; recorded on the run) |
| `--env-file` | `string` | `""` | dotenv file to stage as candidate values for this run only (default: remembered choice; with --from: stored target values; plan only validates the file) |
| `--environment` | `string` | `""` | target environment name (defaults to the checkout binding, prompted interactively otherwise) |
| `--from` | `string` | `""` | promote the active revision of another environment of this project; nothing builds and no manifest is read |
| `--help`, `-h` | `bool` | `false` | help for plan |
| `--manifest` | `string` | `""` | explicit manifest path (otherwise discover skali.yaml or skali.yml up from the working directory) |
| `--pick-env-file` | `bool` | `false` | ask which env file (or the stored values) to use and remember the answer for the environment (not used with --from) |
| `--platform` | `string` | `""` | override the build platform(s), e.g. linux/amd64 or a comma list (default: application platforms intersected with cluster platforms and preference) |
| `--prune-values` | `bool` | `false` | remove stored values the manifest no longer references as part of this deployment |
| `--rebuild` | `bool` | `false` | plan as if artifacts were rebuilt and re-imported without reuse |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali project`

```text
skali project [flags]
```

Manage projects

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for project |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali project remove`

```text
skali project remove [project] [flags]
```

Aliases: `rm`.

Removes every environment of the project exactly as skali env remove would,
with its volumes, databases, buckets, values, revisions, and history, and the
project goes with the last of them. This is one-way. Without a name the
checkout's project is removed. The command follows the removal until the
project is gone; --detach returns right away, and the removal continues on
the server either way. Repeating the command resumes a removal that was
interrupted. Project admin and a recent login are required.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the removal and return without following it |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the typed confirmation |

### `skali remote`

```text
skali remote [flags]
```

Manage named remotes: the skali masters this machine can talk to, each
with its own session. Bare skali remote lists them; skali remote add
creates one and logs in; skali remote login re-authenticates.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali remote add`

```text
skali remote add <name> <host-or-url> [flags]
```

Add a named remote for a skali master and perform the initial login,
like skali remote add example https://skali.example.com. A bare hostname
tries https then http and targets the cluster's /api path
(skali.example.com becomes https://skali.example.com/api); an explicit URL
is used verbatim. On success the new remote becomes the current one; on
failure nothing is stored. The login runs in the cluster's own skali
release, fetched from the release feed on first contact.

In a terminal the login opens the Studio in your browser and waits
for you to approve it there; --no-browser (or SKALI_NO_BROWSER=1) and
non-interactive runs ask for email and password on the terminal instead.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--email` | `string` | `""` | login email (prompted when omitted; implies --no-browser) |
| `--help`, `-h` | `bool` | `false` | help for add |
| `--no-browser` | `bool` | `false` | log in with email and password on the terminal instead of the browser |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali remote list`

```text
skali remote list [flags]
```

Aliases: `ls`.

List remotes

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali remote login`

```text
skali remote login [name] [flags]
```

Re-authenticate an existing remote and store the fresh session token.
Without a name the current remote is used; with one, that remote becomes
current when the login succeeds. Remotes are created with skali remote add.

In a terminal the login opens the Studio in your browser and waits
for you to approve it there; --no-browser (or SKALI_NO_BROWSER=1) and
non-interactive runs ask for email and password on the terminal instead.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--email` | `string` | `""` | login email (prompted when omitted; implies --no-browser) |
| `--help`, `-h` | `bool` | `false` | help for login |
| `--no-browser` | `bool` | `false` | log in with email and password on the terminal instead of the browser |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali remote logout`

```text
skali remote logout [name] [flags]
```

Revoke a remote's session and forget its token

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for logout |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali remote remove`

```text
skali remote remove <name> [flags]
```

Aliases: `rm`.

Remove a remote and revoke its session

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali remote status`

```text
skali remote status [flags]
```

Show the current remote, master reachability, session, and user

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for status |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali remote token`

```text
skali remote token [flags]
```

Print the current remote's session token to stdout, for example:

  curl -H "Authorization: Bearer $(skali remote token)" https://skali.example.com/api/v1/projects

Deploys authenticate to the managed registry automatically with this
session; docker login is only needed for third-party OCI tooling, with
any username and the token as the password.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for token |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali remote use`

```text
skali remote use [name] [flags]
```

Makes the named remote the one workflow commands talk to outside a
bound checkout. Without a name the configured remotes are offered to
pick from.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for use |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali rollback`

```text
skali rollback [flags]
```

Re-points the environment target at a stored revision and rolls it
out exactly as stored: same definition, images, and values. Nothing
is rebuilt and no new revision is created.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--allow-destructive` | `bool` | `false` | allow a rollback that drops a database, bucket, or volume the active revision declares |
| `--detach` | `bool` | `false` | start the rollback and return without attaching to the run |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--help`, `-h` | `bool` | `false` | help for rollback |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--revision` | `string` | `""` | revision to roll back to (a revision id or unique checksum prefix) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation prompt |

### `skali route`

```text
skali route [flags]
```

Routes are the public hostnames of an environment's applications. list
shows each route with its certificate and the reconciler's last verdict
on whether the domain reaches this installation; probe checks the
domains right now, address by address, and asks the reconciler to act
on the result (a domain that arrived triggers a fresh certificate
issuance without waiting for the next pass).

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--help`, `-h` | `bool` | `false` | help for route |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali route list`

```text
skali route list [flags]
```

Aliases: `ls`.

List the environment's routes with their certificate and edge verdict

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) (inherited) |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali route probe`

```text
skali route probe [flags]
```

Resolves every TLS route domain of the environment and asks each address
whether it answers as this installation. The verdicts replace the
reconciler's cached ones: a domain that arrived triggers a fresh
certificate issuance within the next pass, even while cert-manager is
waiting out a failure backoff.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) (inherited) |
| `--help`, `-h` | `bool` | `false` | help for probe |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali run`

```text
skali run [flags]
```

Runs are the journal of platform operations: deployments, rollbacks,
and teardowns. This group lists and inspects them. For running a
project command (seed, migrate) with the application's environment,
see skali dev run.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for run |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali run attach`

```text
skali run attach <run-id> [flags]
```

Renders the run's step tree live until it ends. d detaches and
leaves the run running; Ctrl-C pressed twice cancels it.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for attach |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali run cancel`

```text
skali run cancel <run-id> [flags]
```

Cancels a pending or running run. A promoted but not yet activated
deployment returns the target to the prior active revision. The run
is shown and confirmed first; --yes skips the question.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for cancel |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |

### `skali run list`

```text
skali run list [flags]
```

Aliases: `ls`.

List the environment's runs

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali run logs`

```text
skali run logs <run-id> [flags]
```

Print the logs of one step

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for logs |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--step` | `string` | `""` | step key, e.g. artifacts.web.build **Required.** |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali run show`

```text
skali run show <run-id> [flags]
```

Print a run's step tree

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for show |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali skill`

```text
skali skill [flags]
```

The skill has two parts (docs/versioning.md, decision 6). skill install writes a small operational guide into the agent's skill directory once; skali upgrade refreshes it. skill read prints the version-bound references (the manifest grammar, the CLI surface and platform architecture) and dispatches like every workflow command, so run from a project directory it answers at the release of the project's target cluster.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for skill |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali skill install`

```text
skali skill install [flags]
```

Installs the skali operational skill shell (SKILL.md) into the user-level skill directories of the selected coding agents. The shell tells the agent to read the manifest, architecture and CLI references through skali skill read, which answers at the release of the project's target cluster. The installed directory is owned by this command and is refreshed by skali upgrade.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--agent` | `stringSlice` | `[]` | agent to install for: claude or codex; repeatable |
| `--all` | `bool` | `false` | install for every supported agent |
| `--help`, `-h` | `bool` | `false` | help for install |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

Mutually exclusive: `--agent`, `--all`.

### `skali skill read`

```text
skali skill read [topic] [flags]
```

Prints one of the references coding agents read instead of guessing: manifest (grammar), architecture (platform behavior), or cli (the commands used from the terminal). Without a topic the topics are listed. Run from the project directory the command dispatches to the release of the project's target cluster, so the reference matches the cluster that will compile the manifest. --since <revision> prints the manifest grammar changes since that revision instead of the reference (the same ledger skali validate and skali manifest upgrade consult).

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for read |
| `--manifest` | `string` | `""` | manifest path used to select the checkout |
| `--offline` | `bool` | `false` | use the recorded target release and an available CLI without contacting the remote or downloading |
| `--remote` | `string` | `""` | target remote, overriding the checkout binding and current remote |
| `--since` | `string` | `""` | print the manifest changes since this revision instead of the reference |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali sql`

```text
skali sql [database] [flags]
```

Sql runs SQL against one database of the project, declared as
databases.<database> in skali.yml, connected with that database's own
credentials. Nothing needs to be installed locally and no application
has to run: the platform starts a short-lived database client next to
the environment's applications for the session (a few seconds, longer
while its image is first pulled).

In a terminal, without -c or -f, it opens an interactive psql. Otherwise
it runs a script: the -c text, the -f file (- for stdin), or stdin
itself. Results go to stdout, errors and notices to stderr, and the
exit status follows psql: 0 success, 1 a client failure, 2 the
database unreachable, 3 the script failed (a script stops at its
first error).

--format psql (the default) is psql's own output and understands psql
meta-commands such as \d. --format csv prints psql's CSV without command
tags. --format json prints one document for the whole script:

  {"results": [{"columns": [{"name": "id", "type": "int4", "type_oid": 23}],
                "rows": [[1]], "command": "SELECT 1", "rows_affected": 1}],
   "error": {"code": "42P01", "message": "..."}}

Rows are arrays aligned with the columns; booleans, numbers and json
keep their JSON types, everything else is PostgreSQL's text form; the
output of COPY ... TO STDOUT is kept byte for byte as base64 in
"copy_data" ("copy_encoding": "base64"). The json
format runs the script as one query, so its statements share one
implicit transaction unless the script manages its own; psql
meta-commands and COPY ... FROM STDIN are not available there.

--read-only sets default_transaction_read_only, a guard against
accidental writes rather than a permission. The session needs maintain
on the environment, like exec, and a recent login on a promote-only one.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--command`, `-c` | `string` | `""` | run this SQL and exit (several statements may be separated by semicolons) |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--file`, `-f` | `string` | `""` | run the SQL script in this file and exit; - reads it from stdin |
| `--format` | `string` | `""` | output of a script: psql (default), csv, or json |
| `--help`, `-h` | `bool` | `false` | help for sql |
| `--read-only` | `bool` | `false` | start every transaction read-only (a guard, not a permission) |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--statement-timeout` | `duration` | `0s` | cancel any statement running longer than this, such as 30s (default: the server's) |
| `--timeout` | `duration` | `0s` | end the session after this long, such as 30s or 5m, and exit with status 124 (default: no limit) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali upgrade`

```text
skali upgrade [flags]
```

Replaces this skali binary with a published release: the newest on a
channel (stable, or beta to include alpha, beta, and rc releases), or an
exact --version, which may also downgrade. Downloads are verified against
the release's checksums before anything is written.

The channel defaults to stable, or to beta when this build is itself a
prerelease. A cluster moves with skali cluster upgrade, not this command.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--channel` | `string` | `""` | release channel, stable or beta (default stable; beta for a prerelease build) |
| `--help`, `-h` | `bool` | `false` | help for upgrade |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--version` | `string` | `""` | exact release tag to install (mutually exclusive with --channel; may downgrade) |

### `skali validate`

```text
skali validate [flags]
```

Validate a Skali project manifest and optionally an environment file

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--env-file` | `string` | `""` | dotenv file validated against the manifest's value requirements |
| `--help`, `-h` | `bool` | `false` | help for validate |
| `--manifest` | `string` | `""` | manifest path; defaults to skali.yml or skali.yaml |
| `--offline` | `bool` | `false` | use the recorded target release and an available CLI without contacting the remote or downloading |
| `--remote` | `string` | `""` | target remote, overriding the checkout binding and current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali values`

```text
skali values [flags]
```

Lists the stored values of a remote environment by name and version;
contents are never shown. Values are staged by deployments (--env-file,
or the env file remembered for the environment) and removed by
deploying with --prune-values. For the local platform use skali dev
values.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to list; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for values |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

### `skali version`

```text
skali version [flags]
```

Print the CLI version

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for version |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
