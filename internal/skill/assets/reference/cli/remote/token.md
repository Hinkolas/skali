# skali remote token

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli remote token`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali remote token [flags]
```

Print the current remote's session token to stdout, for example:

  curl -H "Authorization: Bearer $(skali remote token)" https://skali.example.com/api/v1/projects

Deploys authenticate to the managed registry automatically with this
session; docker login is only needed for third-party OCI tooling, with
any username and the token as the password.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for token |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
