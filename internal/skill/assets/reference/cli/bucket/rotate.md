# skali bucket rotate

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli bucket rotate`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/buckets.md): `skali skill read cli workflows buckets`.

```text
skali bucket rotate <bucket> [flags]
```

Issues a new keypair for the bucket named by its manifest key. The
store accepts both keypairs, the applications referencing the bucket
restart with the new one, and after the overlap window the previous
keypair is retired for good: requests and presigned URLs signed with
it fail from that instant on, so --retire-after must cover the longest
URL the application issues (1h by default, 1m for a key known to be
leaked, 7d at most). Processes holding the old keypair outside the
cluster (skali dev host runs, anyone who revealed it) must fetch it
again. Rotating again inside the window retires the older keypair at
once. A bucket a restore is rewriting cannot be rotated until the
restore has finished.

The run streams like a deploy and succeeds once every consumer runs
with the new keypair; the previous keypair retires on schedule either
way. Needs maintain on the environment and a recent login.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the rotation and return without following it |
| `--environment` | `string` | `""` | environment holding the bucket; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for rotate |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--retire-after` | `string` | `1h` | how long the previous keypair stays accepted, such as 30m, 1h, or 2d (at least 1m, at most 7d) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
