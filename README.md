<div align="center">

<img src="website/static/favicon.svg" alt="" width="72" height="72">

# skali

**Run your applications on your own servers, without running a platform team.**

[Website](https://skali.dev) ·
[Releases](https://github.com/Hinkolas/skali/releases) ·
[Documentation](#documentation) ·
[Roadmap](ROADMAP.md)

[![Release](https://img.shields.io/github/v/release/Hinkolas/skali?include_prereleases&sort=semver&label=release)](https://github.com/Hinkolas/skali/releases)
[![CI](https://github.com/Hinkolas/skali/actions/workflows/ci.yml/badge.svg)](https://github.com/Hinkolas/skali/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/Hinkolas/skali)](LICENSE)

</div>

skali is a self-hosted application platform. You describe an application, its
Postgres databases, and its S3 buckets in one `skali.yaml`. skali builds it,
runs it behind TLS routes with health checks and safe rollouts, and hands it
its credentials. The same manifest runs in a disposable local cluster on your
laptop (`skali dev`) and on the servers you install skali on (`skali deploy`).
Underneath it is Kubernetes (k3s), but you never have to touch it.

```sh
curl -fsSL https://skali.dev/install.sh | sh
```

> [!WARNING]
> **skali is in initial development.** Until v1.0.0 the manifest schema, CLI,
> API, and on-disk formats may change in breaking ways, and upgrades may need
> manual steps. Do not use it for workloads you cannot afford to lose. Read the
> [known limitations](docs/limitations.md) and the
> [prerelease safety notes](docs/prerelease-safety.md) first. Project roles do
> not isolate hostile workloads; see [security boundaries](docs/security.md).

## Features

- **Applications** from a Dockerfile or a prebuilt image: replicas, readiness
  and liveness probes, release commands for migrations, and blue-green
  rollouts by default (rolling and recreate selectable).
- **Managed PostgreSQL** (CloudNativePG, with pgvector) and **S3 buckets**
  (SeaweedFS), provisioned from the manifest, with their credentials injected
  as environment variables.
- **Routes with automatic TLS** (Traefik, Let's Encrypt), custom domains, and
  load-balancing strategies. Buckets get their own hostnames.
- **Environments** per project (staging, production, ...), with promotion
  between them, rollbacks, and encrypted per-environment values.
- **Local development on the real platform**: the app you are working on
  hot-reloads on your machine while its databases and buckets run in the local
  cluster.
- **Skali Studio**, a web UI for projects, deployments, runs, metrics, and
  users, next to a full CLI. Users have 2FA and per-project roles.
- **Operations** built in: backups to your own S3 target, cluster updates from
  the Studio or the CLI, diagnosis and repair, single node or many.

## Getting started

### 1. Install the CLI

```sh
curl -fsSL https://skali.dev/install.sh | sh
```

The installer picks the binary for your OS and architecture (macOS and Linux,
amd64 and arm64), verifies its checksum, and installs it to `/usr/local/bin`
on Linux (asking for sudo) or `~/.local/bin` on macOS. It also installs shell
completions for your login shell. Run it on your laptop to develop and
deploy, and on every server that should become a skali node.

The default channel installs the newest stable release. Set
`SKALI_CHANNEL=beta` to include alpha, beta, and RC releases, or
`SKALI_VERSION` to install an exact version:

```sh
curl -fsSL https://skali.dev/install.sh | SKALI_CHANNEL=beta sh
curl -fsSL https://skali.dev/install.sh | SKALI_VERSION=v0.1.0 sh
```

<details>
<summary>macOS: <code>skali: command not found</code></summary>

Add `~/.local/bin` to your PATH. For the default zsh shell:

```sh
export PATH="$HOME/.local/bin:$PATH"
printf '\n%s\n' 'export PATH="$HOME/.local/bin:$PATH"' >> "${ZDOTDIR:-$HOME}/.zshrc"
```

The first line applies to the current terminal, the second to future ones.
The installer prints the right instructions for your shell and never edits
your startup files itself.

</details>

<details>
<summary>Upgrading and shell completions</summary>

```sh
skali upgrade                        # newest release on your channel
skali upgrade --channel beta         # include alpha, beta, and RC releases
skali upgrade --version v0.1.0      # exact release, may also downgrade
```

The channel defaults to stable, or to beta when the installed CLI is itself a
prerelease. Downloads are verified against the release checksums; a system
install on Linux needs `sudo skali upgrade`. This updates the CLI only.
Clusters update through the Studio or `skali cluster upgrade` (see
[cluster updates](docs/updates.md)).

`skali completion install --shell fish` adds completions for another shell,
and `SKALI_COMPLETIONS=none` skips them during installation. To build from
source instead, see [contributor setup](docs/development.md).

</details>

### 2. Run a project locally

All you need is Docker. skali installs a pinned copy of
[k3d](https://k3d.io) for the local cluster if none is on your PATH.

```sh
git clone https://github.com/Hinkolas/skali
cd skali/examples/hello-world
cp .env.example .env
skali dev
```

`skali dev` creates the local platform on first run, builds the project,
deploys it, and follows its logs. The example is served at
`https://hello-world.localhost`, with a certificate from a development CA
generated for your machine. The first run offers to trust that CA
(`skali dev trust` repeats the step).

Ctrl-C pauses the project and keeps its data, `skali dev` brings it back, and
`skali dev -d` runs it in the background. `skali dev status`, `exec`, `list`,
and `reset` do what they say. Add a `dev:` block to an application and
`skali dev` runs that app as a process on your machine with hot reload,
behind the cluster's routes and with its real database and bucket
credentials.

### 3. Set up a server

You need a Debian or Ubuntu server (amd64 or arm64) with ports 80 and 443
reachable. A Mac runs skali inside a managed Lima VM. Point DNS at the server
first: an A record for the platform domain (for example `skali.example.com`),
one for the registry (`cr.skali.example.com`), and one per application
domain.

```sh
curl -fsSL https://skali.dev/install.sh | sh
sudo skali cluster
```

`skali cluster` walks you through creating the cluster and initializing skali
on it with your domains, a Let's Encrypt account email, and the first admin
account. It takes a few minutes and prints the Studio URL at the end. Run
`sudo skali cluster` again at any time for status and the maintenance menu.

For unattended installs, pass config files instead (`skali cluster install
--config node.yaml`, `skali cluster init --config init.yaml`). Their schemas
are published under `https://skali.dev/schemas/v1/` and live in
[`schemas/`](schemas/).

<details>
<summary>Adding nodes</summary>

```sh
# on the server: print a one-use join command
sudo skali cluster token --role agent --capabilities application

# on each new host, after installing the CLI
sudo skali cluster join --token 'skali.…'

# back on the server: review and converge the topology
sudo skali cluster plan
sudo skali cluster apply --wait
```

Nodes declare capabilities (`application`, `database`, `edge`, ...) and skali
places workloads accordingly. Join servers the same way for a highly
available control plane. See [enrollment and recovery](docs/enrollment.md).

</details>

<details>
<summary>Day-two operations</summary>

```sh
sudo skali cluster status          # health of this node and the platform
sudo skali cluster diagnose        # find problems, with suggested fixes
sudo skali cluster repair          # apply them, each one confirmed
sudo skali cluster upgrade --wait  # whole cluster, latest release on its channel
sudo skali cluster reset-password  # recover a locked-out admin account
```

Cluster updates also run from the Studio (System → Software update) and
continue after the CLI disconnects. See [cluster updates](docs/updates.md).

</details>

### 4. Deploy

Connect the CLI to your installation once, then deploy from any project
directory with a `skali.yaml`:

```sh
skali remote add prod skali.example.com   # logs you in
cd my-project
skali deploy
```

The first deploy of a checkout asks which project and environment to target,
and which local env file to deploy with, and remembers both. Every value is
stored encrypted and write-only. Each deploy shows a plan, builds locally,
pushes to the platform's registry, renders the rollout live, and ends with the
URLs of your routes.

```sh
skali plan                        # what a deploy would change
skali logs web                    # live logs of an application
skali exec web -- sh              # a shell in a running container
skali rollback                    # back to the previous revision
skali deploy --from staging       # promote staging's revision here
skali backup create               # snapshot databases, buckets, and volumes
skali access set alice@example.com deploy   # roles: read, deploy, maintain, admin
```

## The manifest

```yaml
# yaml-language-server: $schema=https://skali.dev/schemas/v1/skali.schema.json
name: guestbook

applications:
  web:
    build:
      context: .
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
    environment:
      DATABASE_URL: "{{ databases.data.url }}"
      S3_BUCKET: "{{ buckets.files.name }}"
      S3_ACCESS_KEY: "{{ buckets.files.access_key }}"
      S3_SECRET_KEY: "{{ buckets.files.secret_key }}"
    health:
      readiness:
        http:
          port: http
          path: /healthz
    deployment:
      releaseCommand:
        command: ["./app", "migrate"]
    scaling:
      replicas:
        min: 2

databases:
  data:
    engine: postgres
    version: 17

buckets:
  files:
    quotas:
      storage: 1GB
```

`${NAME}` references are per-environment values you provide. `{{ ... }}`
references are outputs of the databases and buckets skali provisions.
`skali validate` checks a manifest, and the schema line at the top gives you
completion and inline errors in any editor with YAML language support.

[`examples/`](examples/) has complete projects:

| Example | Shows |
| --- | --- |
| [`hello-world`](examples/hello-world) | A minimal build |
| [`whoami`](examples/whoami) | Deploying a prebuilt image |
| [`guestbook`](examples/guestbook) | An app with a database, a bucket, and a volume |
| [`file-sharing`](examples/file-sharing) | Browsers uploading and downloading through presigned bucket URLs |
| [`dev-loop`](examples/dev-loop) | The local hot-reload loop |
| [`deployment-timing`](examples/deployment-timing) | A release migration, a database, and a bucket, with a collector that times each deployment stage |

### With a coding agent

`skali skill install` gives your coding agent the platform's rules for writing
manifests and porting apps. The skill reads the manifest, CLI, and
architecture references through `skali skill read`, which always answers for
the release your project's cluster runs.

## Documentation

| Topic | |
| --- | --- |
| [Databases](docs/databases.md) | Managed PostgreSQL: versions, extensions, isolation, availability |
| [Buckets](docs/buckets.md) | S3 buckets: quotas, hostnames, browser uploads, topology |
| [Storage](docs/storage.md) | Persistent volumes and storage drivers |
| [Permissions](docs/permissions.md) | Users, roles, environments, and protection |
| [Build matrix](docs/build-matrix.md) | Supported Dockerfile and BuildKit features |
| [Enrollment](docs/enrollment.md) | Joining nodes and recovering interrupted joins |
| [Cluster updates](docs/updates.md) | Software updates, exact versions, recovery |
| [Versioning](docs/versioning.md) | How the CLI matches the release of each cluster |
| [Security](docs/security.md) | Trust boundaries and key recovery |
| [Limitations](docs/limitations.md) | What skali does not do yet, and workarounds |
| [Roadmap](ROADMAP.md) | What comes next |

## How it fits together

`skali` is the CLI. It builds on your machine, talks to the platform's API,
and installs and maintains the servers. `skalid` is the control plane on the
cluster: it stores what you declared, compiles it into Kubernetes objects, and
reports status back. Skali Studio is a web app served on the platform domain.
k3s and a small set of operators (CloudNativePG, Traefik, cert-manager,
SeaweedFS, optionally Longhorn) do the generic orchestration. skali owns the
hosts it runs on and never adopts a cluster it did not install.

## Contributing

Issues and pull requests are welcome. [`docs/development.md`](docs/development.md)
covers the components, local setup, and the test suites. Please report
security issues privately as described in [SECURITY.md](SECURITY.md).

## License

skali is released under the [Apache License 2.0](LICENSE). You can run it for
yourself, your company, or your customers, modify it, and redistribute it.
