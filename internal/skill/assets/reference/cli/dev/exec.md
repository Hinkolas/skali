# skali dev exec

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev exec`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/databases.md): `skali skill read cli workflows databases`.

```text
skali dev exec [service] [flags] -- <command>...
```

Exec opens a command or interactive shell inside a running container of
one service on the local platform, like docker exec. Without a command
it starts /bin/sh.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--container` | `string` | `""` | container name (default: the service's own container) |
| `--help`, `-h` | `bool` | `false` | help for exec |
| `--no-stdin`, `-n` | `bool` | `false` | do not forward stdin: the command sees end of input at once (like ssh -n); implies --no-tty |
| `--no-tty` | `bool` | `false` | never allocate a remote TTY |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--pod` | `string` | `""` | exec into this pod instead of the newest ready one |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--timeout` | `duration` | `0s` | end the session after this long, such as 30s or 5m, and exit with status 124 (default: no limit) |
| `--tty`, `-t` | `bool` | `false` | allocate a remote TTY even when stdout is not a terminal |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
