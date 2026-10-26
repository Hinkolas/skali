# skali remote login

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli remote login`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali remote login [name] [flags]
```

Re-authenticate an existing remote and store the fresh session token.
Without a name the current remote is used; with one, that remote becomes
current when the login succeeds. Remotes are created with skali remote add.

In a terminal the login opens the Studio in your browser and waits
for you to approve it there; --no-browser (or SKALI_NO_BROWSER=1) and
non-interactive runs ask for email and password on the terminal instead.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--email` | `string` | `""` | login email (prompted when omitted; implies --no-browser) |
| `--help`, `-h` | `bool` | `false` | help for login |
| `--no-browser` | `bool` | `false` | log in with email and password on the terminal instead of the browser |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
