# skali cluster

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster`.
[CLI overview](../cli.md). [Workflow guide](workflows/cluster.md): `skali skill read cli workflows cluster`.

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

## Subcommands

- [`skali cluster addresses`](cluster/addresses.md): Declare how this host is addressed inside and outside the cluster
- [`skali cluster apply`](cluster/apply.md): Apply every pending cluster topology change
- [`skali cluster changes`](cluster/changes.md): Manage the candidate cluster revision
- [`skali cluster create`](cluster/create.md): Create a reconciled cluster seed and coordinator
- [`skali cluster diagnose`](cluster/diagnose.md): Diagnose this installation from host state and the Kubernetes API
- [`skali cluster init`](cluster/init.md): Initialize Skali on the joined cluster (run once on a server)
- [`skali cluster install`](cluster/install.md): Install k3s and prepare this host as a Skali node
- [`skali cluster join`](cluster/join.md): Join this host to an existing cluster
- [`skali cluster node`](cluster/node.md): Stage reconciled-cluster node changes
- [`skali cluster plan`](cluster/plan.md): Preview the candidate cluster transition
- [`skali cluster rebalance`](cluster/rebalance.md): Reconcile the current topology and optionally redistribute workloads
- [`skali cluster repair`](cluster/repair.md): Diagnose and repair this installation with scoped, confirmed actions
- [`skali cluster reset-password`](cluster/reset-password.md): Reset a user's password from a server node
- [`skali cluster status`](cluster/status.md): Show the installation state and health of this host
- [`skali cluster tier`](cluster/tier.md): Apply the available database availability tier
- [`skali cluster token`](cluster/token.md): Print the join command for this cluster
- [`skali cluster uninstall`](cluster/uninstall.md): Remove the Skali bundle or this node, with scoped confirmation
- [`skali cluster upgrade`](cluster/upgrade.md): Update the whole managed cluster to one Skali release
