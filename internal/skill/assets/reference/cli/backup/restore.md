# skali backup restore

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup restore`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup restore [snapshot-id] [flags]
```

Verifies the snapshot's data, stops the environment, replaces every
matching database, bucket, and volume with the snapshot's data, then
resumes the current revision. Current data is overwritten. The
environment must be deployed first; restore moves data, not
configuration. A restore that stops the environment and does not
complete keeps it down and refuses deploys until a restore
completes, or until its data is accepted (skali backup accept).

Without a snapshot id the project's snapshots are offered to pick
from. The snapshot restores into the environment it was taken from
unless --environment names another environment of the same project.
The resolved remote, project, snapshot, and environment are shown
and confirmed by typing the environment name; --yes skips that.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to restore into; defaults to the environment the snapshot was taken from |
| `--help`, `-h` | `bool` | `false` | help for restore |
| `--project` | `string` | `""` | project holding the snapshot; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the typed confirmation |
