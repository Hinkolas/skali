# skali database

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli database`.
[CLI overview](../cli.md). [Workflow guide](workflows/databases.md): `skali skill read cli workflows databases`.

```text
skali database [flags]
```

Managed PostgreSQL databases and the pools they run on. `rotate` acts
on one database of an environment, declared in skali.yml. `list`,
`show` and `set` inspect and tune the pools: every pool runs a
parameter set derived from its memory budget (automatic from the
smallest database node, or set explicitly) with per-parameter
overrides on top; those three are admin only.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for database |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali database list`](database/list.md): List the database pools with their budgets
- [`skali database rotate`](database/rotate.md): Issue new credentials for a database and retire the current ones
- [`skali database set`](database/set.md): Change a database pool's memory budget or parameter overrides
- [`skali database show`](database/show.md): Show a database pool's budget and effective parameters
