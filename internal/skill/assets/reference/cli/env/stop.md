# skali env stop

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli env stop`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/access.md): `skali skill read cli workflows access`.

```text
skali env stop <name> [flags]
```

Takes the running workloads down. The namespace with its volumes, the values,
revisions, and history stay, and the next deploy starts the environment
again. The command follows the stop until it is done; --detach returns
right away, and running it again while the stop is in progress follows that
stop. Environment admin and a recent login are required.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the stop and return without following it |
| `--help`, `-h` | `bool` | `false` | help for stop |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
