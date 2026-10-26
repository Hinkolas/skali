# skali skill

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli skill`.
[CLI overview](../cli.md). [Workflow guide](workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali skill [flags]
```

The skill has two parts (docs/versioning.md, decision 6). skill install writes a small operational guide into the agent's skill directory once; skali upgrade refreshes it. skill read prints the version-bound references (the manifest grammar, the CLI surface and platform architecture) and dispatches like every workflow command, so run from a project directory it answers at the release of the project's target cluster.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for skill |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali skill install`](skill/install.md): Install the skali skill for coding agents
- [`skali skill read`](skill/read.md): Print a skali reference at the target's release
