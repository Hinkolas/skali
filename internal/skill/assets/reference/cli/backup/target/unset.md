# skali backup target unset

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup target unset`.
[CLI overview](../../../cli.md). [Workflow guide](../../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup target unset [flags]
```

Removes the backup target and its stored credentials. Admin only.
Snapshots already written to the bucket are untouched; setting the
same target again makes them listable and restorable once more.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for unset |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
