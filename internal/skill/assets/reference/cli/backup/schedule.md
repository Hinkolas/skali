# skali backup schedule

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup schedule`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup schedule [flags]
```

Automatic backups are an environment setting: every database, bucket,
and volume of the environment is snapshotted on a cron schedule (UTC),
and snapshots the schedule takes are deleted once older than the
retention (the newest is always kept). Manual snapshots never expire.
Changing the schedule needs environment admin and a recent login.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for schedule |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali backup schedule remove`](schedule/remove.md): Turn automatic backups of an environment off
- [`skali backup schedule set`](schedule/set.md): Turn automatic backups on or change their schedule
- [`skali backup schedule show`](schedule/show.md): Show the automatic backup schedule of an environment
