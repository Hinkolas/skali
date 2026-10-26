# skali access set

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli access set`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/access.md): `skali skill read cli workflows access`.

```text
skali access set <user> <role> [flags]
```

Without --environment sets the user's project role (read, deploy, maintain,
admin), adding them as a member when needed. With --environment sets an
explicit role on that environment only (none locks it for them); the user
must already be a member. <user> is an email address or a user id.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | set the role on this environment only |
| `--help`, `-h` | `bool` | `false` | help for set |
| `--project` | `string` | `""` | project to manage; defaults to the checkout's project |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
