# skali validate

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli validate`.
[CLI overview](../cli.md). [Workflow guide](workflows/deployments.md): `skali skill read cli workflows deployments`.

```text
skali validate [flags]
```

Validate a Skali project manifest and optionally an environment file

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--env-file` | `string` | `""` | dotenv file validated against the manifest's value requirements |
| `--help`, `-h` | `bool` | `false` | help for validate |
| `--manifest` | `string` | `""` | manifest path; defaults to skali.yml or skali.yaml |
| `--offline` | `bool` | `false` | use the recorded target release and an available CLI without contacting the remote or downloading |
| `--remote` | `string` | `""` | target remote, overriding the checkout binding and current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
