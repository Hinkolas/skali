# skali manifest

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli manifest`.
[CLI overview](../cli.md). [Workflow guide](workflows/deployments.md): `skali skill read cli workflows deployments`.

```text
skali manifest [flags]
```

Work on the project manifest

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for manifest |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali manifest upgrade`](manifest/upgrade.md): Clean up obsolete fields and update local manifest review history
