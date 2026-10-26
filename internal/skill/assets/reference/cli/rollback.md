# skali rollback

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli rollback`.
[CLI overview](../cli.md). [Workflow guide](workflows/deployments.md): `skali skill read cli workflows deployments`.

```text
skali rollback [flags]
```

Re-points the environment target at a stored revision and rolls it
out exactly as stored: same definition, images, and values. Nothing
is rebuilt and no new revision is created.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--allow-destructive` | `bool` | `false` | allow a rollback that drops a database, bucket, or volume the active revision declares |
| `--detach` | `bool` | `false` | start the rollback and return without attaching to the run |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) |
| `--help`, `-h` | `bool` | `false` | help for rollback |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--revision` | `string` | `""` | revision to roll back to (a revision id or unique checksum prefix) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation prompt |
