# skali run cancel

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli run cancel`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/runs.md): `skali skill read cli workflows runs`.

```text
skali run cancel <run-id> [flags]
```

Cancels a pending or running run. A promoted but not yet activated
deployment returns the target to the prior active revision. The run
is shown and confirmed first; --yes skips the question.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for cancel |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
