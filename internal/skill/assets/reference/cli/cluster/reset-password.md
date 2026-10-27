# skali cluster reset-password

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster reset-password`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

```text
skali cluster reset-password [flags]
```

Reset a user's password without knowing the old one. This is the recovery
path when nobody can sign in anymore: it runs on a server node with root
access and needs no working login. Every session of the user is revoked;
--disable-2fa also removes a lost authenticator.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--disable-2fa` | `bool` | `false` | also remove the user's two-factor enrollment |
| `--email` | `string` | `""` | login email of the user (prompted when omitted) |
| `--help`, `-h` | `bool` | `false` | help for reset-password |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--password-stdin` | `bool` | `false` | read the new password from stdin instead of prompting |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
