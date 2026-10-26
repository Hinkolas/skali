# skali backup

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup`.
[CLI overview](../cli.md). [Workflow guide](workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup [flags]
```

Back up and restore environment data

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for backup |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali backup accept`](backup/accept.md): Accept the data an incomplete restore left, so the environment can deploy again
- [`skali backup create`](backup/create.md): Snapshot the environment's data to the backup target
- [`skali backup list`](backup/list.md): List the project's snapshots on the backup target
- [`skali backup remove`](backup/remove.md): Delete a snapshot from the backup target
- [`skali backup restore`](backup/restore.md): Restore a snapshot's data into an environment
- [`skali backup schedule`](backup/schedule.md): Configure automatic backups of an environment
- [`skali backup target`](backup/target.md): Manage the external S3 backup target
