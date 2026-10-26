# skali run logs

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli run logs`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/runs.md): `skali skill read cli workflows runs`.

```text
skali run logs <run-id> [flags]
```

Print the logs of one step

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for logs |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--step` | `string` | `""` | step key, e.g. artifacts.web.build **Required.** |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
