# skali env

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli env`.
[CLI overview](../cli.md). [Workflow guide](workflows/access.md): `skali skill read cli workflows access`.

```text
skali env [flags]
```

Environments of the checkout's project (or --project). Listing needs
project read, creating needs project maintain (the creator becomes
admin of the new environment), settings, stop, and removal need environment
admin and a recent login. Automatic backups are configured with
skali backup schedule.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for env |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali env create`](env/create.md): Create an environment
- [`skali env list`](env/list.md): List the project's environments with your access and their settings
- [`skali env remove`](env/remove.md): Remove an environment with its workloads, volumes, values, revisions, and history
- [`skali env rename`](env/rename.md): Rename an environment without changing its data or identity
- [`skali env set`](env/set.md): Change an environment's ceiling, deploy policy, or priority
- [`skali env stop`](env/stop.md): Stop an environment's workloads but keep its data
