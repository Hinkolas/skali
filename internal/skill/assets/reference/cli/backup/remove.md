# skali backup remove

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup remove`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup remove <snapshot-id> [flags]
```

Aliases: `rm`.

Removes one snapshot of the project from the backup target: its
manifest first, then every database dump, bucket copy, and volume
archive it holds. This cannot be undone. Manual snapshots are only
ever removed this way; snapshots an environment's schedule took also
expire under that environment's retention. A snapshot a restore is
currently reading is refused.

The resolved remote, project, and snapshot are shown and confirmed
before anything is deleted; --yes skips the question. Needs maintain
on the environment the snapshot was taken from and a recent login.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--project` | `string` | `""` | project holding the snapshot; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
