# skali access list

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli access list`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/access.md): `skali skill read cli workflows access`.

```text
skali access list [flags]
```

Aliases: `ls`.

Show members and their effective role on every environment

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for list |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
