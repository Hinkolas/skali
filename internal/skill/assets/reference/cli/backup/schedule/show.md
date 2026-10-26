# skali backup schedule show

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup schedule show`.
[CLI overview](../../../cli.md). [Workflow guide](../../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup schedule show [flags]
```

Prints whether automatic backups of the bound environment (or
--environment) are on, their schedule, retention, and next fire.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to show; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for show |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
