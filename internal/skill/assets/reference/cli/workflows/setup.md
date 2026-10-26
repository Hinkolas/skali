# Remotes, CLI installation, and completions

Read with `skali skill read cli workflows setup`.
[CLI overview and command index](../../cli.md).

`skali remote list` displays incomplete entries and `remote remove NAME`
can remove them. Invalid YAML diagnostics identify the configuration file to edit.

`skali remote add <name> <host-or-url>` registers and logs in; `remote use
[name]` selects the default for unbound checkouts, and `remote login [name]`
refreshes authentication. Browser login is the default on a terminal;
`--no-browser`, `SKALI_NO_BROWSER=1`, or `--email` selects terminal login.
`remote status` reports reachability and the session. `remote logout [name]`
revokes/forgets credentials but keeps the remote, whereas `remote remove
<name>` removes it. `remote token` prints the stored bearer token to stdout
for API scripting; treat that output as a secret.

`skali upgrade` updates the CLI, managed installed skills, and managed shell
completions. `--channel stable|beta` selects a channel; `--version` selects an
exact tag and cannot be combined with `--channel`. A prerelease CLI defaults
to beta; otherwise stable. This does not upgrade a local or managed cluster.
`skali version` reports the invoked binary and recorded remotes/cache state.

`skali skill install --agent codex`, `--agent claude`, or `--all` installs the
version-neutral guide. Agent selection is required without an interactive
terminal; `--agent` is repeatable and cannot be combined with `--all`. Managed
directories are refreshed/pruned; user-owned skills are refused.
`skali completion <shell>` prints a script; `completion install --shell <shell>`
installs it for bash, zsh, fish, or PowerShell. The default is the login shell
(`SKALI_SHELL` overrides detection). Dynamic completion does not download CLIs.
The installed scripts complete environments, remotes, run IDs, manifest
commands, and reference pages. `install.sh` installs completions too.
