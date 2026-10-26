# skali skill install

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli skill install`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali skill install [flags]
```

Installs the skali operational skill shell (SKILL.md) into the user-level skill directories of the selected coding agents. The shell tells the agent to read the manifest, architecture and CLI references through skali skill read, which answers at the release of the project's target cluster. The installed directory is owned by this command and is refreshed by skali upgrade.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--agent` | `stringSlice` | `[]` | agent to install for: claude or codex; repeatable |
| `--all` | `bool` | `false` | install for every supported agent |
| `--help`, `-h` | `bool` | `false` | help for install |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

Mutually exclusive: `--agent`, `--all`.
