# skali route

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli route`.
[CLI overview](../cli.md). [Workflow guide](workflows/routes.md): `skali skill read cli workflows routes`.

```text
skali route [flags]
```

Routes are the public hostnames of an environment's applications. list
shows each route with its certificate and the reconciler's last verdict
on whether the domain reaches this installation; probe checks the
domains right now, address by address, and asks the reconciler to act
on the result (a domain that arrived triggers a fresh certificate
issuance without waiting for the next pass).

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--help`, `-h` | `bool` | `false` | help for route |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali route list`](route/list.md): List the environment's routes with their certificate and edge verdict
- [`skali route probe`](route/probe.md): Check right now whether the route domains reach this installation
