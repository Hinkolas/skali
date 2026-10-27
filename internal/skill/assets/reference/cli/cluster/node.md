# skali cluster node

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster node`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

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

## Subcommands

- [`skali cluster node capabilities`](node/capabilities.md): Stage a node capability set
- [`skali cluster node forget`](node/forget.md): Finalize an unreachable removed host without local cleanup
- [`skali cluster node remove`](node/remove.md): Stage a managed node for drain and removal
- [`skali cluster node restore`](node/restore.md): Cancel a staged node removal
