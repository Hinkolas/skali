# Cluster installation and maintenance

Read with `skali skill read cli workflows cluster`.
[CLI overview and command index](../../cli.md).

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
