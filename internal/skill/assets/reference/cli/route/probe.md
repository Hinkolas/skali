# skali route probe

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli route probe`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/routes.md): `skali skill read cli workflows routes`.

```text
skali route probe [flags]
```

Resolves every TLS route domain of the environment and asks each address
whether it answers as this installation. The verdicts replace the
reconciler's cached ones: a domain that arrived triggers a fresh
certificate issuance within the next pass, even while cert-manager is
waiting out a failure backoff.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment name (default: the checkout binding) (inherited) |
| `--help`, `-h` | `bool` | `false` | help for probe |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
