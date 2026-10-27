# skali cluster repair

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster repair`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

```text
skali cluster repair [flags]
```

Diagnose and repair this installation with scoped, confirmed actions

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for repair |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | confirm every planned repair action |
