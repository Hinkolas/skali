# skali run show

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli run show`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/runs.md): `skali skill read cli workflows runs`.

```text
skali run show <run-id> [flags]
```

Print a run's step tree

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for show |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
