# skali cluster uninstall

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster uninstall`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

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
