# skali cluster changes import

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster changes import`.
[CLI overview](../../../cli.md). [Workflow guide](../../workflows/cluster.md): `skali skill read cli workflows cluster`.

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
