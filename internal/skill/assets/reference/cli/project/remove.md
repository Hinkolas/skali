# skali project remove

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli project remove`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/access.md): `skali skill read cli workflows access`.

```text
skali project remove [project] [flags]
```

Aliases: `rm`.

Removes every environment of the project exactly as skali env remove would,
with its volumes, databases, buckets, values, revisions, and history, and the
project goes with the last of them. This is one-way. Without a name the
checkout's project is removed. The command follows the removal until the
project is gone; --detach returns right away, and the removal continues on
the server either way. Repeating the command resumes a removal that was
interrupted. Project admin and a recent login are required.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the removal and return without following it |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the typed confirmation |
