# skali backup accept

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli backup accept`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/backups.md): `skali skill read cli workflows backups`.

```text
skali backup accept [flags]
```

A restore that stops an environment and does not complete holds it:
the environment stays down and deployments are refused, because its
data may be partial. Re-running a restore (skali backup restore) is
the way back to complete data. When that is not possible, accept
the data as it is: components the restore rewrote may hold partial
data, and the others still hold what they held before.

Accepting lifts the hold only; the environment stays down until the
next deploy. The restore is shown and confirmed by typing the
environment name; --yes skips that.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment the restore holds; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for accept |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the typed confirmation |
