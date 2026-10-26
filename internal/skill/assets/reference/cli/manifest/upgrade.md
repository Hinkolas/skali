# skali manifest upgrade

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli manifest upgrade`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/deployments.md): `skali skill read cli workflows deployments`.

```text
skali manifest upgrade [flags]
```

Validates safe manifest edits before writing. Relevant semantic changes require explicit review and --acknowledge. Review history is stored in .skali/, never in the manifest.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--acknowledge` | `bool` | `false` | confirm review of relevant semantic changes |
| `--help`, `-h` | `bool` | `false` | help for upgrade |
| `--manifest` | `string` | `""` | manifest path; defaults to skali.yml or skali.yaml |
| `--offline` | `bool` | `false` | use the recorded target release and an available CLI without contacting the remote or downloading |
| `--remote` | `string` | `""` | target remote, overriding the checkout binding and current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
