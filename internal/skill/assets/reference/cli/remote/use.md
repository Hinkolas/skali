# skali remote use

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli remote use`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali remote use [name] [flags]
```

Makes the named remote the one workflow commands talk to outside a
bound checkout. Without a name the configured remotes are offered to
pick from.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for use |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
