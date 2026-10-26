# skali cluster upgrade

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster upgrade`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

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
