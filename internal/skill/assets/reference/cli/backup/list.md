# skali backup list

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup list`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup list [flags]
```

Aliases: `ls`.

Lists the snapshots of every environment of the project, newest
first, as the backup target holds them; --environment narrows the
list to one environment. Snapshots of environments that no longer
exist are listed too and can be restored into another environment.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | only list snapshots of this environment |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--project` | `string` | `""` | project whose snapshots to list; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
