# skali backup target set

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup target set`.
[CLI overview](../../../cli.md). [Workflow guide](../../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup target set [flags]
```

Stores the external S3 location backups are written to. Admin only;
the secret access key is write-only and never shown again. Without
--secret-key the key is prompted (or read from
SKALI_BACKUP_SECRET_KEY).

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--access-key` | `string` | `""` | S3 access key id **Required.** |
| `--bucket` | `string` | `""` | bucket backups are written into **Required.** |
| `--endpoint` | `string` | `""` | S3 endpoint URL, e.g. https://s3.example.com **Required.** |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--prefix` | `string` | `""` | key prefix inside the bucket |
| `--region` | `string` | `""` | S3 region; empty for providers that ignore it |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--secret-key` | `string` | `""` | S3 secret access key (prefer the prompt or SKALI_BACKUP_SECRET_KEY) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
