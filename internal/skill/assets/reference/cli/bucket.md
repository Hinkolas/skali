# skali bucket

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli bucket`.
[CLI overview](../cli.md). [Workflow guide](workflows/buckets.md): `skali skill read cli workflows buckets`.

```text
skali bucket [flags]
```

Managed object storage buckets of an environment. Buckets are declared
in skali.yml; this group holds the operations on a provisioned bucket
that are not configuration.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for bucket |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali bucket rotate`](bucket/rotate.md): Issue a new S3 keypair for a bucket and retire the current one
