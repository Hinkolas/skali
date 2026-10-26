# skali env create

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli env create`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/access.md): `skali skill read cli workflows access`.

```text
skali env create <name> [flags]
```

Creates an environment in the project; you become its admin. Priority high
(instance admins only) marks an environment that keeps running when
resources are tight; it starts with a read ceiling for inheriting members.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for create |
| `--priority` | `string` | `""` | normal (default) or high (instance admins only) |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
