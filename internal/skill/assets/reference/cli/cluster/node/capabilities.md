# skali cluster node capabilities

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster node capabilities`.
[CLI overview](../../../cli.md). [Workflow guide](../../workflows/cluster.md): `skali skill read cli workflows cluster`.

```text
skali cluster node capabilities <node> <capability>... [flags]
```

Stage a node capability set

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for capabilities |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
