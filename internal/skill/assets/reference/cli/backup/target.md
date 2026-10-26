# skali backup target

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup target`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup target [flags]
```

Manage the external S3 backup target

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for target |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali backup target set`](target/set.md): Configure where backups are written
- [`skali backup target show`](target/show.md): Show the configured backup target
- [`skali backup target unset`](target/unset.md): Remove the configured backup target
