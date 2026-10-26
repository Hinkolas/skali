# skali logs

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli logs`.
[CLI overview](../cli.md). [Workflow guide](workflows/runs.md): `skali skill read cli workflows runs`.

```text
skali logs [service] [flags]
```

Stream live application logs

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--help`, `-h` | `bool` | `false` | help for logs |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
