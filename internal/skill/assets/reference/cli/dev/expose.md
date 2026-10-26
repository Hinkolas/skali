# skali dev expose

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev expose`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/development.md): `skali skill read cli workflows development`.

```text
skali dev expose [address] [flags]
```

Opens this machine's local platform to skali dev proxy on another machine.
The platform's edge only listens on loopback; expose listens on ports 443
and 80 of one address of this machine, such as its LAN or VPN address, and
relays raw TCP to the edge. It prints the skali dev proxy command to run on
the other machine, including the CA fingerprint.

Anyone who reaches that address reaches the development edge: the Studio
and every route of every project on the local platform. Pick an address
only your own machines reach; the wildcard address is refused. The address
is remembered: a bare skali dev expose reuses it. -d runs it in the
background; skali dev expose stop ends it. Tailscale users can run
tailscale serve instead. On macOS, sudo asks for your password to open
the ports, and expose keeps running as you.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach`, `-d` | `bool` | `false` | run in the background; skali dev expose stop ends it |
| `--help`, `-h` | `bool` | `false` | help for expose |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali dev expose stop`](expose/stop.md): Stop exposing the local platform
