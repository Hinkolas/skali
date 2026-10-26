# skali env remove

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli env remove`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/access.md): `skali skill read cli workflows access`.

```text
skali env remove <name> [flags]
```

Aliases: `rm`.

Purges the environment: the namespace with its volumes, the databases and
buckets with their data, and the environment with all values, revisions, and
history are deleted. This is one-way. The command follows the removal until
the environment is gone; --detach returns right away, and running it again
while the removal is in progress follows that removal instead of restarting
it. Environment admin and a recent login are required.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the removal and return without following it |
| `--help`, `-h` | `bool` | `false` | help for remove |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the typed confirmation |
