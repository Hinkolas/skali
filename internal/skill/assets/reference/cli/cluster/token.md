# skali cluster token

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster token`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

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

## Subcommands

- [`skali cluster token list`](token/list.md): List reconciled-cluster invitations
- [`skali cluster token revoke`](token/revoke.md): Revoke an unused reconciled-cluster invitation
