# skali backup schedule set

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup schedule set`.
[CLI overview](../../../cli.md). [Workflow guide](../../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup schedule set [flags]
```

Sets the automatic backup schedule of the bound environment (or
--environment): --every is a five-field cron expression evaluated in
UTC, --keep how long the snapshots it takes are kept (7d, 4w, 12h; at
least one minute). The first snapshot runs at the next fire of the
schedule, never right away.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to schedule; defaults to the checkout binding |
| `--every` | `string` | `""` | five-field cron expression in UTC, for example "0 3 * * *" **Required.** |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--keep` | `string` | `""` | how long snapshots the schedule takes are kept, for example 7d or 4w **Required.** |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
