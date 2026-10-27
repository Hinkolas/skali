# skali cluster rebalance

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster rebalance`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

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
| `--wait` | `bool` | `false` | wait for rebalancing to complete |
| `--workloads` | `bool` | `false` | roll eligible managed workloads to use the current topology |
| `--yes` | `bool` | `false` | rebalance without an interactive confirmation |
