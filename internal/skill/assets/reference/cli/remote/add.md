# skali remote add

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli remote add`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/setup.md): `skali skill read cli workflows setup`.

```text
skali remote add <name> <host-or-url> [flags]
```

Add a named remote for a skali master and perform the initial login,
like skali remote add example https://skali.example.com. A bare hostname
tries https then http and targets the cluster's /api path
(skali.example.com becomes https://skali.example.com/api); an explicit URL
is used verbatim. On success the new remote becomes the current one; on
failure nothing is stored. The login runs in the cluster's own skali
release, fetched from the release feed on first contact.

In a terminal the login opens the Studio in your browser and waits
for you to approve it there; --no-browser (or SKALI_NO_BROWSER=1) and
non-interactive runs ask for email and password on the terminal instead.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--email` | `string` | `""` | login email (prompted when omitted; implies --no-browser) |
| `--help`, `-h` | `bool` | `false` | help for add |
| `--no-browser` | `bool` | `false` | log in with email and password on the terminal instead of the browser |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
