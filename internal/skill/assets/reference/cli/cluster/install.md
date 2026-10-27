# skali cluster install

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster install`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

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
