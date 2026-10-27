# skali cluster status

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster status`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

```text
skali cluster status [flags]
```

Show the installation state and health of this host

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--all` | `bool` | `false` | include cancelled and removed node history |
| `--help`, `-h` | `bool` | `false` | help for status |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
