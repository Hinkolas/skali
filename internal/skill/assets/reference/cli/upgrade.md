# skali upgrade

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli upgrade`.
[CLI overview](../cli.md). [Workflow guide](workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali upgrade [flags]
```

Replaces this skali binary with a published release: the newest on a
channel (stable, or beta to include alpha, beta, and rc releases), or an
exact --version, which may also downgrade. Downloads are verified against
the release's checksums before anything is written.

The channel defaults to stable, or to beta when this build is itself a
prerelease. A cluster moves with skali cluster upgrade, not this command.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--channel` | `string` | `""` | release channel, stable or beta (default stable; beta for a prerelease build) |
| `--help`, `-h` | `bool` | `false` | help for upgrade |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--version` | `string` | `""` | exact release tag to install (mutually exclusive with --channel; may downgrade) |
