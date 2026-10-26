# skali database list

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli database list`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/databases.md): `skali skill read cli workflows databases`.

```text
skali database list [flags]
```

Aliases: `ls`.

List the database pools with their budgets

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
