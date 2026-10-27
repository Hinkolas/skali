# skali run insights

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli run insights`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/runs.md): `skali skill read cli workflows runs`.

```text
skali run insights <run-id> [flags]
```

Prints what each local build of the run reported once it ended: its
outcome and duration, how many of its steps were cached, and its
slowest steps. --log adds the builder's full output. A build that
was interrupted, or ran with a CLI or server from before build
insights, has none.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for insights |
| `--log` | `bool` | `false` | also print the builder's full output |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--step` | `string` | `""` | one build step's key, e.g. artifacts.web.build (default: every build of the run) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
