# skali database rotate

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli database rotate`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/databases.md): `skali skill read cli workflows databases`.

```text
skali database rotate <database> [flags]
```

Issues a new login role for the database named by its manifest key. The
pool accepts both roles, the applications referencing the database
restart with the new username and password, and after the overlap
window the previous login role is retired for good: its open sessions
are terminated and the role is dropped (1h by default, 1m for a
password known to be leaked, 7d at most). The database and everything
in it stay owned by the owner role the login roles act as, so
migrations and restores are unaffected. Processes holding the old
credentials outside the cluster (skali dev host runs, anyone who
revealed them) must fetch them again. Rotating again inside the window
retires the older login role at once.

The run streams like a deploy and succeeds once every consumer runs
with the new credentials; the previous login role retires on schedule
either way. Needs maintain on the environment and a recent login.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach` | `bool` | `false` | start the rotation and return without following it |
| `--environment` | `string` | `""` | environment holding the database; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for rotate |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--retire-after` | `string` | `1h` | how long the previous login role stays accepted, such as 30m, 1h, or 2d (at least 1m, at most 7d) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | skip the confirmation |
