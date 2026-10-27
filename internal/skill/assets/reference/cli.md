# Working with the Skali CLI

Start with this overview and command index, then read the command and workflow
needed for the task:

```sh
skali skill read cli deploy
skali skill read cli cluster node remove
skali skill read cli workflows deployments
```

A command page includes its synopsis, aliases, every public local/inherited flag,
defaults, flag combinations, immediate subcommands, and links to workflow advice.
`cli/deploy` is an equivalent single-argument topic. Use canonical command names
from the index; a command alias is listed on its page. Group pages list their
children without printing every descendant's flags.

Run reference commands from the project directory with the same `--remote` and
`--manifest` selectors as the task. The header identifies the answering release,
target, selection source, and online/offline status. The checkout binding takes
precedence over the current remote; no target means the invoked CLI answers.
`--offline` requires the recorded release's CLI locally and does not verify the
cluster's current release. References never fall back to another release.
Reread the overview and relevant pages when the target or release changes.

For a target whose overview has no command-page links, read its combined CLI
reference instead. Older binaries keep their embedded documentation; an
unavailable page is not permission to use another release's reference.

`skali --help` and `skali <command> --help` are always local and prefer cached
target help. Their header identifies any fallback to home help; help does not
verify compatibility. `skali skill read` lists the top-level reference topics.

## Reading flags

Only flags listed on a command's own page apply to it. `--remote`, `--environment`,
`--manifest`, `--offline`, and `--yes` are not universal. `--` ends flag parsing
before an exec or host command's arguments. Empty string defaults mean no
explicit value; help explains runtime defaults. `stringSlice` accepts commas
or repeated flags, while `stringArray` takes one value per occurrence. Boolean
flags support `--flag=false`. `duration` uses Go syntax such as `1h30m`; string
flags such as `--keep` and `--retire-after` use their documented Skali units.

<!-- BEGIN GENERATED COMMAND CATALOG -->
## Workflow guides

Read the relevant guide for behavior spanning multiple commands. Each command
page links to its guide. `skali skill read cli workflows` lists these guides.

- [Deployments, values, and stored revisions](cli/workflows/deployments.md): `skali skill read cli workflows deployments`
- [Logs and runs](cli/workflows/runs.md): `skali skill read cli workflows runs`
- [Remotes, CLI installation, and completions](cli/workflows/setup.md): `skali skill read cli workflows setup`
- [Cluster installation and maintenance](cli/workflows/cluster.md): `skali skill read cli workflows cluster`
- [Local development](cli/workflows/development.md): `skali skill read cli workflows development`
- [Backups](cli/workflows/backups.md): `skali skill read cli workflows backups`
- [Buckets](cli/workflows/buckets.md): `skali skill read cli workflows buckets`
- [Databases, SQL, and container exec](cli/workflows/databases.md): `skali skill read cli workflows databases`
- [Routes](cli/workflows/routes.md): `skali skill read cli workflows routes`
- [Access and environments](cli/workflows/access.md): `skali skill read cli workflows access`

## Global invocation and flags

```text
skali [flags]
```

Deploy apps and manage Skali clusters

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for skali |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics |
| `--version`, `-v` | `bool` | `false` | version for skali |

## Command index

For `skali A B`, read `skali skill read cli A B`. All public commands are listed;
each link opens only that command's reference. Global flags are above.

- [`skali access`](cli/access.md): Manage who may do what on a project
- [`skali access list`](cli/access/list.md): Show members and their effective role on every environment
- [`skali access remove`](cli/access/remove.md): Remove a member, or drop their explicit role on one environment
- [`skali access set`](cli/access/set.md): Set a member's project role, or their explicit role on one environment
- [`skali backup`](cli/backup.md): Back up and restore environment data
- [`skali backup accept`](cli/backup/accept.md): Accept the data an incomplete restore left, so the environment can deploy again
- [`skali backup create`](cli/backup/create.md): Snapshot the environment's data to the backup target
- [`skali backup list`](cli/backup/list.md): List the project's snapshots on the backup target
- [`skali backup remove`](cli/backup/remove.md): Delete a snapshot from the backup target
- [`skali backup restore`](cli/backup/restore.md): Restore a snapshot's data into an environment
- [`skali backup schedule`](cli/backup/schedule.md): Configure automatic backups of an environment
- [`skali backup schedule remove`](cli/backup/schedule/remove.md): Turn automatic backups of an environment off
- [`skali backup schedule set`](cli/backup/schedule/set.md): Turn automatic backups on or change their schedule
- [`skali backup schedule show`](cli/backup/schedule/show.md): Show the automatic backup schedule of an environment
- [`skali backup target`](cli/backup/target.md): Manage the external S3 backup target
- [`skali backup target set`](cli/backup/target/set.md): Configure where backups are written
- [`skali backup target show`](cli/backup/target/show.md): Show the configured backup target
- [`skali backup target unset`](cli/backup/target/unset.md): Remove the configured backup target
- [`skali bucket`](cli/bucket.md): Operate the environment's managed buckets
- [`skali bucket rotate`](cli/bucket/rotate.md): Issue a new S3 keypair for a bucket and retire the current one
- [`skali cluster`](cli/cluster.md): Install, maintain, and recover a Skali cluster
- [`skali cluster addresses`](cli/cluster/addresses.md): Declare how this host is addressed inside and outside the cluster
- [`skali cluster apply`](cli/cluster/apply.md): Apply every pending cluster topology change
- [`skali cluster changes`](cli/cluster/changes.md): Manage the candidate cluster revision
- [`skali cluster changes discard`](cli/cluster/changes/discard.md): Discard every unapplied candidate change
- [`skali cluster changes import`](cli/cluster/changes/import.md): Replace the candidate using already-enrolled nodes from a layout
- [`skali cluster create`](cli/cluster/create.md): Create a reconciled cluster seed and coordinator
- [`skali cluster diagnose`](cli/cluster/diagnose.md): Diagnose this installation from host state and the Kubernetes API
- [`skali cluster init`](cli/cluster/init.md): Initialize Skali on the joined cluster (run once on a server)
- [`skali cluster install`](cli/cluster/install.md): Install k3s and prepare this host as a Skali node
- [`skali cluster join`](cli/cluster/join.md): Join this host to an existing cluster
- [`skali cluster node`](cli/cluster/node.md): Stage reconciled-cluster node changes
- [`skali cluster node capabilities`](cli/cluster/node/capabilities.md): Stage a node capability set
- [`skali cluster node forget`](cli/cluster/node/forget.md): Finalize an unreachable removed host without local cleanup
- [`skali cluster node remove`](cli/cluster/node/remove.md): Stage a managed node for drain and removal
- [`skali cluster node restore`](cli/cluster/node/restore.md): Cancel a staged node removal
- [`skali cluster plan`](cli/cluster/plan.md): Preview the candidate cluster transition
- [`skali cluster rebalance`](cli/cluster/rebalance.md): Reconcile the current topology and optionally redistribute workloads
- [`skali cluster repair`](cli/cluster/repair.md): Diagnose and repair this installation with scoped, confirmed actions
- [`skali cluster reset-password`](cli/cluster/reset-password.md): Reset a user's password from a server node
- [`skali cluster status`](cli/cluster/status.md): Show the installation state and health of this host
- [`skali cluster storage-migrate`](cli/cluster/storage-migrate.md): Move the registry volume onto the replicated storage class
- [`skali cluster tier`](cli/cluster/tier.md): Apply the available database availability tier
- [`skali cluster token`](cli/cluster/token.md): Print the join command for this cluster
- [`skali cluster token list`](cli/cluster/token/list.md): List reconciled-cluster invitations
- [`skali cluster token revoke`](cli/cluster/token/revoke.md): Revoke an unused reconciled-cluster invitation
- [`skali cluster uninstall`](cli/cluster/uninstall.md): Remove the Skali bundle or this node, with scoped confirmation
- [`skali cluster upgrade`](cli/cluster/upgrade.md): Update the whole managed cluster to one Skali release
- [`skali compile`](cli/compile.md): Compile a Skali manifest into canonical IR, a revision, or Kubernetes objects
- [`skali completion`](cli/completion.md): Print or install shell completions
- [`skali completion bash`](cli/completion/bash.md): Print the bash completion script
- [`skali completion fish`](cli/completion/fish.md): Print the fish completion script
- [`skali completion install`](cli/completion/install.md): Install the completion script where your shell loads it
- [`skali completion powershell`](cli/completion/powershell.md): Print the powershell completion script
- [`skali completion zsh`](cli/completion/zsh.md): Print the zsh completion script
- [`skali database`](cli/database.md): Operate the managed databases and their pools
- [`skali database list`](cli/database/list.md): List the database pools with their budgets
- [`skali database rotate`](cli/database/rotate.md): Issue new credentials for a database and retire the current ones
- [`skali database set`](cli/database/set.md): Change a database pool's memory budget or parameter overrides
- [`skali database show`](cli/database/show.md): Show a database pool's budget and effective parameters
- [`skali deploy`](cli/deploy.md): Deploy the project to an environment
- [`skali dev`](cli/dev.md): Run the project on the local Skali platform
- [`skali dev down`](cli/dev/down.md): Remove the project from the local platform; data is retained
- [`skali dev exec`](cli/dev/exec.md): Run a command in a running app container
- [`skali dev expose`](cli/dev/expose.md): Let skali dev proxy on another machine reach this machine's local platform
- [`skali dev expose stop`](cli/dev/expose/stop.md): Stop exposing the local platform
- [`skali dev list`](cli/dev/list.md): List projects on the local platform
- [`skali dev logs`](cli/dev/logs.md): Stream the local project's runtime logs
- [`skali dev proxy`](cli/dev/proxy.md): Open a skali dev platform on another machine at its *.localhost origins
- [`skali dev proxy stop`](cli/dev/proxy/stop.md): Stop the proxy running in the background
- [`skali dev reset`](cli/dev/reset.md): Destroy the local dev platform completely
- [`skali dev run`](cli/dev/run.md): Run a command on the host with an application's resolved environment
- [`skali dev sql`](cli/dev/sql.md): Run SQL against a database of the local platform
- [`skali dev start`](cli/dev/start.md): Start the local platform, creating it when absent
- [`skali dev status`](cli/dev/status.md): Show platform and current-project state
- [`skali dev stop`](cli/dev/stop.md): Stop the running local platform; state is retained
- [`skali dev trust`](cli/dev/trust.md): Trust a skali dev platform's development CA in this machine's browsers
- [`skali dev up`](cli/dev/up.md): Build, deploy, and attach to the current project (same as bare skali dev)
- [`skali dev upgrade`](cli/dev/upgrade.md): Upgrade the local platform in place, keeping its data and development CA
- [`skali dev values`](cli/dev/values.md): List the local project's stored values by name and version
- [`skali env`](cli/env.md): List and configure environments
- [`skali env create`](cli/env/create.md): Create an environment
- [`skali env list`](cli/env/list.md): List the project's environments with your access and their settings
- [`skali env remove`](cli/env/remove.md): Remove an environment with its workloads, volumes, values, revisions, and history
- [`skali env rename`](cli/env/rename.md): Rename an environment without changing its data or identity
- [`skali env set`](cli/env/set.md): Change an environment's ceiling, deploy policy, or priority
- [`skali env stop`](cli/env/stop.md): Stop an environment's workloads but keep its data
- [`skali exec`](cli/exec.md): Run a command in a running app container
- [`skali help`](cli/help.md): Help about any command
- [`skali logs`](cli/logs.md): Stream live application logs
- [`skali manifest`](cli/manifest.md): Work on the project manifest
- [`skali manifest upgrade`](cli/manifest/upgrade.md): Clean up obsolete fields and update local manifest review history
- [`skali plan`](cli/plan.md): Preview the deployment plan without building or rolling out workloads
- [`skali project`](cli/project.md): Manage projects
- [`skali project remove`](cli/project/remove.md): Remove a project with every environment and all its data
- [`skali remote`](cli/remote.md): Manage the skali masters this machine talks to
- [`skali remote add`](cli/remote/add.md): Add a remote and log in to it
- [`skali remote list`](cli/remote/list.md): List remotes
- [`skali remote login`](cli/remote/login.md): Log in again (browser, or email and password with --no-browser)
- [`skali remote logout`](cli/remote/logout.md): Revoke a remote's session and forget its token
- [`skali remote remove`](cli/remote/remove.md): Remove a remote and revoke its session
- [`skali remote status`](cli/remote/status.md): Show the current remote, master reachability, session, and user
- [`skali remote token`](cli/remote/token.md): Print the stored session token (for scripting API requests)
- [`skali remote use`](cli/remote/use.md): Switch the current remote
- [`skali rollback`](cli/rollback.md): Roll the environment back to a previous revision
- [`skali route`](cli/route.md): List the environment's routes and probe their domains
- [`skali route list`](cli/route/list.md): List the environment's routes with their certificate and edge verdict
- [`skali route probe`](cli/route/probe.md): Check right now whether the route domains reach this installation
- [`skali run`](cli/run.md): List, inspect, attach to, or cancel runs
- [`skali run attach`](cli/run/attach.md): Attach the terminal to a run until it settles
- [`skali run cancel`](cli/run/cancel.md): Cancel a run
- [`skali run insights`](cli/run/insights.md): Print the insights of a run's builds
- [`skali run list`](cli/run/list.md): List the environment's runs
- [`skali run logs`](cli/run/logs.md): Print the logs of one step
- [`skali run show`](cli/run/show.md): Print a run's step tree
- [`skali skill`](cli/skill.md): Manage the skali skill for coding agents
- [`skali skill install`](cli/skill/install.md): Install the skali skill for coding agents
- [`skali skill read`](cli/skill/read.md): Print a skali reference at the target's release
- [`skali sql`](cli/sql.md): Run SQL against a database
- [`skali upgrade`](cli/upgrade.md): Update this skali CLI to a published release
- [`skali validate`](cli/validate.md): Validate a Skali project manifest and optionally an environment file
- [`skali values`](cli/values.md): List the environment's stored values by name and version
- [`skali version`](cli/version.md): Print the CLI version
