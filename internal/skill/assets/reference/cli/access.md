# skali access

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli access`.
[CLI overview](../cli.md). [Workflow guide](workflows/access.md): `skali skill read cli workflows access`.

```text
skali access [flags]
```

Project access is one role ladder, none < read < deploy < maintain < admin,
used at two levels: a member's project role applies to every environment
unless an explicit per-environment role (set with --environment) overrides
it, and an environment's ceiling (skali env set --max-role) caps inherited
roles. Listing needs project read; changes need project admin (environment
admin for --environment) and a recent login.

The project is the checkout's (its binding or manifest) unless --project
names one.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for access |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali access list`](access/list.md): Show members and their effective role on every environment
- [`skali access remove`](access/remove.md): Remove a member, or drop their explicit role on one environment
- [`skali access set`](access/set.md): Set a member's project role, or their explicit role on one environment
