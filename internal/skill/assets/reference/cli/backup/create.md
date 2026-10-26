# skali backup create

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup create`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup create [flags]
```

Backs up every database, bucket, and application volume of the
environment to the configured S3 target as one complete snapshot.
Configuration and secret values are not included: a snapshot
restores data into a redeployed environment.

The resolved remote, project, and environment are shown and
confirmed before the snapshot starts; --yes skips the question.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the backup and return without following it |
| `--environment` | `string` | `""` | environment to back up; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for create |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
