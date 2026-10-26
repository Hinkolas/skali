# skali backup schedule remove

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup schedule remove`.
[CLI overview](../../../cli.md). [Workflow guide](../../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup schedule remove [flags]
```

Aliases: `rm`.

Turns automatic backups of the bound environment (or --environment)
off. Snapshots already taken stay on the backup target and no longer
expire; manual backups keep working.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to change; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
