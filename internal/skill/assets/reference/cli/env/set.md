# skali env set

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli env set`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/access.md): `skali skill read cli workflows access`.

```text
skali env set [flags]
```

Changes settings of the bound environment (or --environment); only the
flags given change. --max-role caps the roles members inherit from their
project role (explicit per-environment roles and project admins are not
capped). --deploy-policy promote-only refuses direct deploys; --promote-from
names the environments promotions may come from (any to allow every
environment of the project). Raising --priority to high is instance admin
only.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--deploy-policy` | `string` | `""` | direct or promote-only |
| `--environment` | `string` | `""` | environment to change; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--max-role` | `string` | `""` | ceiling for inherited roles: none, read, deploy, maintain, or admin |
| `--priority` | `string` | `""` | normal or high |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--promote-from` | `string` | `""` | comma-separated source environments for promotions, or any |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
