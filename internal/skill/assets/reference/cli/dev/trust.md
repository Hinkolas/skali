# skali dev trust

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev trust`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/development.md): `skali skill read cli workflows development`.

```text
skali dev trust [host] [flags]
```

The local platform serves every route over https from a development CA
generated for this installation. skali itself trusts it; browsers need it
in the OS trust store. skali dev offers this once after creating the
platform; this command installs it (again) or, with --check, only reports
where it is trusted. Installing is an addition of one certificate and safe
to repeat; macOS asks for the login password, Linux for sudo.

With a host, it trusts the CA of a skali dev platform on another machine
instead, fetched from the edge reachable at that host (port 443). --ca is the
fingerprint skali dev status prints on that machine, and the CA is accepted
only when it matches. Use this when something else already forwards the
remote edge to this machine, such as ssh -L for ports 443 and 80, with
localhost as the host; skali dev proxy trusts the CA by itself.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--ca` | `string` | `""` | with a host: the fingerprint of that platform's development CA, as skali dev status prints it there (sha256:…) |
| `--check` | `bool` | `false` | report the trust state without installing anything |
| `--help`, `-h` | `bool` | `false` | help for trust |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
