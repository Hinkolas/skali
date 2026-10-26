# skali remote

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli remote`.
[CLI overview](../cli.md). [Workflow guide](workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali remote [flags]
```

Manage named remotes: the skali masters this machine can talk to, each
with its own session. Bare skali remote lists them; skali remote add
creates one and logs in; skali remote login re-authenticates.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali remote add`](remote/add.md): Add a remote and log in to it
- [`skali remote list`](remote/list.md): List remotes
- [`skali remote login`](remote/login.md): Log in again (browser, or email and password with --no-browser)
- [`skali remote logout`](remote/logout.md): Revoke a remote's session and forget its token
- [`skali remote remove`](remote/remove.md): Remove a remote and revoke its session
- [`skali remote status`](remote/status.md): Show the current remote, master reachability, session, and user
- [`skali remote token`](remote/token.md): Print the stored session token (for scripting API requests)
- [`skali remote use`](remote/use.md): Switch the current remote
