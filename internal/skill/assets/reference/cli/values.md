# skali values

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli values`.
[CLI overview](../cli.md). [Workflow guide](workflows/deployments.md): `skali skill read cli workflows deployments`.

```text
skali values [flags]
```

Lists the stored values of a remote environment by name and version;
contents are never shown. Values are staged by deployments (--env-file,
or the env file remembered for the environment) and removed by
deploying with --prune-values. For the local platform use skali dev
values.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--environment` | `string` | `""` | environment to list; defaults to the checkout binding |
| `--help`, `-h` | `bool` | `false` | help for values |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
