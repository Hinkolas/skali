# skali run attach

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli run attach`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/runs.md): `skali skill read cli workflows runs`.

```text
skali run attach <run-id> [flags]
```

Renders the run's step tree live until it ends. d detaches and
leaves the run running; Ctrl-C pressed twice cancels it.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for attach |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
