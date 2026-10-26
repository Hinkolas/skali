# skali run

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli run`.
[CLI overview](../cli.md). [Workflow guide](workflows/runs.md): `skali skill read cli workflows runs`.

```text
skali run [flags]
```

Runs are the journal of platform operations: deployments, rollbacks,
and teardowns. This group lists and inspects them. For running a
project command (seed, migrate) with the application's environment,
see skali dev run.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for run |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali run attach`](run/attach.md): Attach the terminal to a run until it settles
- [`skali run cancel`](run/cancel.md): Cancel a run
- [`skali run list`](run/list.md): List the environment's runs
- [`skali run logs`](run/logs.md): Print the logs of one step
- [`skali run show`](run/show.md): Print a run's step tree
