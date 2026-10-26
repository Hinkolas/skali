# skali database set

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli database set`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/databases.md): `skali skill read cli workflows databases`.

```text
skali database set <name> [flags]
```

Retunes one pool. --memory sets an explicit budget (a Kubernetes
quantity such as 4Gi); --auto-memory returns it to the automatic size.
--set key=value adds or replaces one override in PostgreSQL syntax
(work_mem=64MB); --unset key removes one. Parameters that reload live
take effect without interruption; the ones that need a restart
(shared_buffers, max_connections, wal_buffers, max_worker_processes)
restart the pool's instances, so a single-instance pool is briefly
unavailable. Admin only, sudo mode.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--auto-memory` | `bool` | `false` | size the budget automatically from the smallest database node |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--memory` | `string` | `""` | explicit memory budget, e.g. 4Gi |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--set` | `stringArray` | `[]` | override one parameter, key=value in PostgreSQL syntax (repeatable) |
| `--unset` | `stringArray` | `[]` | remove one override (repeatable) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes`, `-y` | `bool` | `false` | skip the confirmation prompt |
