# skali route list

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli route list`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/routes.md): `skali skill read cli workflows routes`.

```text
skali route list [flags]
```

Aliases: `ls`.

List the environment's routes with their certificate and edge verdict

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) (inherited) |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
