# skali completion

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli completion`.
[CLI overview](../cli.md). [Workflow guide](workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali completion [flags]
```

Shell completions complete skali's commands and flags and, where a
value has a known set, the values too: remotes, environments, projects,
run and snapshot ids, members, the manifest's applications and named
commands, and fixed choices such as roles and channels. Remote-backed
values come from the remote the command line points at and give up
silently after two seconds.

skali completion install writes the script where your shell loads it;
install.sh runs it for your login shell. The shell subcommands print
the script for a manual setup.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for completion |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali completion bash`](completion/bash.md): Print the bash completion script
- [`skali completion fish`](completion/fish.md): Print the fish completion script
- [`skali completion install`](completion/install.md): Install the completion script where your shell loads it
- [`skali completion powershell`](completion/powershell.md): Print the powershell completion script
- [`skali completion zsh`](completion/zsh.md): Print the zsh completion script
