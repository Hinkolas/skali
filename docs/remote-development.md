# Remote development

When your checkout and `skali dev` run on another machine, such as a
workstation, a Mac mini, or a cloud VM, and your browser runs on a laptop,
`skali dev proxy` makes the laptop the entrypoint. The browser on the laptop
opens `https://<domain>.localhost` and gets the remote platform: the same
origins, the same certificates, and no cluster or Docker on the laptop.

## How it works

```
laptop                                       remote machine
browser ─ 127.0.0.1:443 ─ skali dev proxy ── <host>:443 ─ door ─ 127.0.0.1:443  skali dev edge
```

- The remote platform's edge listens only on that machine's loopback ports
  443 and 80, like every local platform.
- A door on the remote machine passes outside traffic to it:
  `skali dev expose <address>` or Tailscale Serve.
- `skali dev proxy <host>` on the laptop listens on the laptop's loopback
  ports 443 and 80 and relays raw TCP to the same ports on `<host>`. TLS ends
  at the remote edge and nothing is rewritten, so SNI, `Host`, `Origin`,
  cookies, WebSockets, and server-sent events behave exactly as they do on the
  remote machine. Each connection dials anew, so the proxy keeps working after
  the laptop sleeps or changes networks.
- Browsers on the laptop must trust the remote platform's development CA. The
  proxy fetches it from `<host>` and accepts it only when it matches `--ca`,
  the fingerprint the remote machine prints, then installs it like
  `skali dev trust` does. macOS asks for the login password once.

The fingerprint is required, not a courtesy check. A trusted root CA has no
name constraints: whoever holds the key of a wrongly installed root can
impersonate any HTTPS site to the laptop, long after they left your network.
The proxy also never prints the fingerprint it saw on a mismatch.

How the laptop reaches `<host>` is up to you: a LAN, a VPN, or a tailnet all
work. The setups below are the common ones.

## Get the command

On the remote machine, `skali dev status` prints the command to run on the
laptop:

```
remote     skali dev proxy <host> --ca sha256:4be1…
```

The real line carries the full fingerprint. While `skali dev expose` runs,
`<host>` is filled in with its address. `skali dev trust --check` prints the
same fingerprint. Host and fingerprint are remembered on the laptop, so a bare
`skali dev proxy` reuses them afterwards.

## Tailscale

Both machines join the same tailnet. Tailscale only routes traffic for tailnet
addresses through its tunnel; everything else on the laptop is untouched. On
the remote machine, publish the edge on its tailnet address once. The rules
persist across restarts:

```sh
tailscale serve --bg --tcp=443 tcp://127.0.0.1:443
tailscale serve --bg --tcp=80  tcp://127.0.0.1:80
```

On the laptop, use the remote machine's MagicDNS name:

```sh
skali dev proxy mac-mini --ca sha256:…
```

Tailscale Serve holds one handler per port. If another tool already serves
HTTPS on port 443 of the remote machine's tailnet address, such as T3 Code's
**Tailscale HTTPS** setting, turn that off: the proxy always connects to ports
443 and 80. `tailscale serve status` lists the rules.

## Local network

On the remote machine, find its LAN address (`ipconfig getifaddr en0` on
macOS, `ip -4 addr` on Linux) and expose the edge there:

```sh
skali dev expose 192.168.1.20 -d
```

It prints the complete proxy command, fingerprint included. On the laptop:

```sh
skali dev proxy 192.168.1.20 --ca sha256:…
```

Anyone who reaches that address reaches the development edge: the Studio and
every route of every project on the platform, with their development data.
Use this on networks you trust. `skali dev expose` refuses the wildcard
address for the same reason.

## WireGuard or another VPN

Expose the edge on the remote machine's VPN address, so only VPN peers reach
it:

```sh
skali dev expose 10.8.0.2 -d
```

On the laptop, run `skali dev proxy 10.8.0.2 --ca sha256:…`.

## SSH, without the proxy

When the laptop can SSH to the remote machine, SSH can forward the edge
ports by itself. Nothing is exposed on the remote machine, and it works over
any network that reaches SSH, including through a jump host (`-J`). The
laptop then only needs the CA:

```sh
ssh -N -L 127.0.0.1:443:127.0.0.1:443 -L 127.0.0.1:80:127.0.0.1:80 user@mac-mini
skali dev trust localhost --ca sha256:…
```

The forward lives as long as the SSH connection, which ends when the network
drops or the laptop sleeps. `autossh` reconnects it.

## In the background

`skali dev proxy -d` and `skali dev expose -d` keep running after the
terminal closes. They open their ports before they go to the background, so a
password prompt on macOS appears in the terminal. `skali dev proxy stop` and `skali dev expose stop` end them,
`skali dev status` shows them, and their output goes to `proxy.log` and
`expose.log` in `~/.local/state/skali/remote-dev/`. They do not start again
after a reboot by themselves.

## Ports on macOS

macOS lets only administrators listen on ports below 1024 of a specific
address. When `skali dev proxy` or `skali dev expose` starts, it opens its two
ports through `sudo`, which asks for your login password:

```
macOS lets only administrators listen on 127.0.0.1:443 and 127.0.0.1:80. sudo opens them for skali dev proxy, which keeps running as you; it may ask for your login password.
Password:
```

Only a small helper runs as root. It opens the ports on exactly those
addresses, hands them to the proxy, and exits. The proxy keeps running as you,
so the CA goes into your login keychain, its state stays in your home
directory, and `skali dev proxy stop` and `skali dev status` work without
`sudo`. Do not run the proxy itself with `sudo`.

Your account must be an administrator of the Mac. With `-d`, the password is
asked once in the terminal and the background process takes over the open
ports. Without a terminal, such as in a script, `sudo` cannot ask, and the
proxy stops with an error that says so.

## When the remote platform is reset

`skali dev reset` on the remote machine creates a new development CA. The
proxy then refuses to start because the CA no longer matches. Run
`skali dev status` there again and pass the new `--ca` once. The old CA stays
in the laptop's keychain but signs nothing anymore, because its key was
deleted with the old platform. Search Keychain Access for "skali local dev CA"
to remove it.

## Limits

- The laptop serves one remote platform at a time, and cannot run a local
  platform of its own while the proxy holds ports 443 and 80. `skali dev`
  refuses to start one and names the proxy.
- Commands that talk to the platform, such as `skali dev logs`, `exec`, and
  `run`, and the project section of `skali dev status`, run on the remote
  machine. The laptop holds no credentials for it.
- On Linux, listening on ports below 1024 needs privileges. Allow it with
  `sudo sysctl -w net.ipv4.ip_unprivileged_port_start=80`, and a file in
  `/etc/sysctl.d` keeps it.
- On macOS, only administrators can listen on ports below 1024 of a specific
  address, such as the proxy's `127.0.0.1:443` and `127.0.0.1:80` or the
  address `skali dev expose` listens on. See [Ports on macOS](#ports-on-macos).
