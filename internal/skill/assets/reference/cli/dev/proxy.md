# skali dev proxy

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev proxy`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/development.md): `skali skill read cli workflows development`.

```text
skali dev proxy [host] [flags]
```

Makes this machine the entrypoint of a skali dev platform that runs on
another machine, without a cluster or Docker here. The proxy listens on
this machine's ports 443 and 80 and relays raw TCP to the same ports on
host, so https://<domain>.localhost in a browser here is the same origin
it is on the other machine: TLS ends at the remote edge and nothing is
rewritten.

The remote edge only listens on that machine's loopback, so something there
has to pass outside traffic to it: skali dev expose <address>, or tailscale
serve. How this machine reaches host (a LAN, a VPN, a tailnet) is up to you.

Browsers here must trust the remote platform's development CA. The proxy
fetches it from host and accepts it only when it matches --ca, the
fingerprint skali dev status prints on the other machine, then installs it
like skali dev trust. Host and fingerprint are remembered: a bare skali dev
proxy reuses them. -d runs the proxy in the background; skali dev proxy
stop ends it.

macOS lets only administrators listen on ports 443 and 80 of loopback:
sudo asks for your password and opens them, and the proxy keeps running
as you.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--ca` | `string` | `""` | fingerprint of the remote platform's development CA, as skali dev status prints it there (sha256:…); remembered after the first run |
| `--detach`, `-d` | `bool` | `false` | run in the background; skali dev proxy stop ends it |
| `--help`, `-h` | `bool` | `false` | help for proxy |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali dev proxy stop`](proxy/stop.md): Stop the proxy running in the background
