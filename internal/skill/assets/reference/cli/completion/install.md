# skali completion install

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli completion install`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali completion install [flags]
```

Writes the completion script for your login shell (or --shell) into the
directory the shell already scans, so no startup file changes: bash
loads ~/.local/share/bash-completion/completions, fish
~/.config/fish/completions, and zsh a site-functions directory on its
fpath, Homebrew's when it is writable. Without one, the script lands in
~/.local/share/zsh/site-functions and the command prints the fpath line
to add before compinit in ~/.zshrc.

An installed script is refreshed by skali upgrade. New shells pick it
up; running shells need a restart (zsh: rm ~/.zcompdump first if the
new commands do not appear).

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for install |
| `--shell` | `string` | `""` | shell to install for: bash, zsh, or fish (default: your login shell) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
