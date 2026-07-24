# Remote management transcripts

Context: remotes are the named Skali installations this machine can talk to,
stored in `~/.config/skali/config.yaml` together with the session token minted
for each. The `skali remote` group is the only owner of that inventory:
`remote add` creates an entry and performs the initial login, `remote login`
re-authenticates an existing entry, and the `local` remote belongs to
`skali dev` alone.

## 1. Add the first remote

```console
$ skali remote add https://skali.example.com
◆ Email
│ dana@example.com
◆ Password
│ entered
logged in to https://skali.example.com as dana@example.com (remote "skali.example.com")
```

The name defaults to the URL host, including any non-standard port
(`http://localhost:7070` becomes `localhost:7070`). The new remote becomes
the current one. A failed add creates nothing: the URL is validated and the
master probed for reachability before any credential prompt, and the entry is
only written after the login succeeds.

## 2. Add a second remote with an explicit name and TOTP

```console
$ skali remote add https://staging.example.com --name staging --email dana@example.com
◆ Password
│ entered
◆ Two-factor code
│ 123456
logged in to https://staging.example.com as dana@example.com (remote "staging")
```

`--name` overrides the derived name; the name `local` is refused because it is
reserved for the local development platform. Text and masked fields accept
Left/Right, Home/End, Backspace, and Delete while active; passwords are never
copied into settled output.

## 3. List and switch

Bare `skali remote` lists; `*` marks the current remote:

```console
$ skali remote
  skali.example.com  https://skali.example.com  [logged in]
* staging            https://staging.example.com  [logged in]

$ skali remote use skali.example.com
switched to remote "skali.example.com"
```

## 4. Status

```console
$ skali remote status
remote:  skali.example.com
master:  https://skali.example.com
health:  ok
user:    dana@example.com (Dana)
session: valid, expires 2026-07-29 14:02
```

Status covers reachability, session validity, and identity in one command.
When the master is unreachable, status stops after the health line.

## 5. Re-authentication after expiry

```console
$ skali remote status
remote:  skali.example.com
master:  https://skali.example.com
health:  ok
session: expired or revoked; run `skali remote login`

$ skali remote login
◆ Email
│ dana@example.com
◆ Password
│ entered
logged in to https://skali.example.com as dana@example.com (remote "skali.example.com")
```

`skali remote login staging` logs in to a named remote and makes it current on
success. Login never creates a remote; an unknown name points at
`skali remote add <url>`.

## 6. Registry login

```console
$ skali remote token | docker login registry.example.com -u dana@example.com --password-stdin
Login Succeeded
```

The session token doubles as the docker password for the managed registry;
grants are scoped server-side.

## 7. Remove, and the reserved local remote

```console
$ skali remote remove staging
removed remote "staging"

$ skali remote remove local
error: remote "local" is managed by skali dev; run `skali dev reset` to remove the local platform
$ echo $?
1
```

Removing a logged-in remote revokes its session server-side on a best-effort
basis before the entry is deleted. The `local` remote is created and refreshed
by `skali dev up` (its bootstrap credentials are machine-local, so
`skali remote login local` is never needed) and only `skali dev reset`
removes it.
