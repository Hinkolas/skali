# skali skill read

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli skill read`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali skill read [topic] [command...] [flags]
```

Prints one of the references coding agents read instead of guessing: manifest (grammar), architecture (platform behavior), or cli (the command overview). Read one command with 'skill read cli deploy' or 'skill read cli cluster node remove'; 'skill read cli workflows' lists cross-command guidance. Slash-separated topics such as cli/deploy also work. Without a topic the top-level topics are listed. Run from the project directory the command dispatches to the release of the project's target cluster, so the reference matches the cluster that will compile the manifest. --since <revision> prints the manifest grammar changes since that revision instead of the reference (the same ledger skali validate and skali manifest upgrade consult).

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for read |
| `--manifest` | `string` | `""` | manifest path used to select the checkout |
| `--offline` | `bool` | `false` | use the recorded target release and an available CLI without contacting the remote or downloading |
| `--remote` | `string` | `""` | target remote, overriding the checkout binding and current remote |
| `--since` | `string` | `""` | print the manifest changes since this revision instead of the reference |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
