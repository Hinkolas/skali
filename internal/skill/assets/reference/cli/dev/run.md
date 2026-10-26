# skali dev run

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev run`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/development.md): `skali skill read cli workflows development`.

```text
skali dev run [app] [name] -- <command>... [flags]
```

Runs a named command from the manifest (applications.<app>.commands)
or a raw command after --, on this machine in the project root, with
the application's fully resolved environment: stored values plus
database and bucket outputs rewritten to the local platform's
loopback ports. Seeds and migrations against the dev database live
here. Bare skali dev run lists the declared commands, skali dev run
<app> those of one application. For a shell inside the running
container, see skali dev exec; skali run manages journal runs, not
project commands.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for run |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
