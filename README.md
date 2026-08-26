# skali

Run your applications on your own servers, without running a platform team.

skali is a self-hosted hosting platform. You describe an application, its
Postgres databases, and its S3 buckets in one `skali.yaml`; skali builds it,
runs it with TLS routes, health checks, and rollouts, and hands you the
credentials. The same manifest runs on a disposable local cluster on your
laptop (`skali dev`) and on the servers you install skali on (`skali
deploy`). Under the hood it is Kubernetes (k3s), but you never have to touch
it.

- **Applications** from a Dockerfile or a prebuilt image, replicated, with
  readiness and liveness probes, release commands for migrations, and
  zero-downtime rollouts.
- **Managed PostgreSQL** (CloudNativePG) and **S3 buckets** (SeaweedFS),
  provisioned from the manifest with credentials injected as environment
  variables.
- **Routes with automatic TLS** (Traefik, Let's Encrypt), custom domains,
  load-balancing strategies.
- **Environments** per project (staging, production, ...), promotion between
  them, rollbacks, encrypted per-environment values.
- **Local development** on the real platform: hot reload for the app you are
  working on while its databases and buckets run in the local cluster.
- **A web console** and a CLI, users with 2FA, and per-project roles.
- **Backups** of environment data to your own S3 target, cluster upgrades,
  diagnosis and repair, single node or many.

**Status:** early. skali is used daily for development and staging;
production hardening is in progress and tracked in [`ROADMAP.md`](ROADMAP.md).

## Install the CLI

```sh
curl -fsSL https://skali.dev/install.sh | sh
```

The script picks the binary for your OS and architecture (macOS and Linux,
amd64 and arm64), verifies its checksum, and installs it: `/usr/local/bin`
on Linux (asks for sudo), `~/.local/bin` on macOS. Set `SKALI_VERSION=v0.1.0`
to pin a release. Run it on your laptop to develop and deploy, and on every
server that should become a skali node.

## Run a project locally

Requirements: Docker. skali installs a pinned copy of
[k3d](https://k3d.io) for the local cluster if none is on your PATH.

```sh
git clone https://github.com/Hinkolas/skali
cd skali/examples/hello-world
cp .env.example .env
skali dev
```

`skali dev` creates the local platform on first run, builds the project,
deploys it, and follows its logs. The example is then served at
`http://hello-world.localhost:8080`. Ctrl-C pauses the project (data is kept),
`skali dev` brings it back, `skali dev -d` keeps it running in the
background. `skali dev ls`, `skali dev status`, `skali dev exec`, and
`skali dev reset` do what they say; reset is the only destructive one and
asks first.

Add a `dev:` block to an application and bare `skali dev` runs that app as a
process on your machine with hot reload, behind the cluster's routes and with
its real database and bucket credentials.

## Set up a server

Any Linux server with a public IP works; a Mac runs skali inside a managed
Lima VM. Point DNS at the server first: an A record for the platform domain
(for example `skali.example.com`), one for the registry
(`cr.skali.example.com`), and one per application domain. Ports 80 and 443
must be reachable.

```sh
curl -fsSL https://skali.dev/install.sh | sh
sudo skali cluster
```

`skali cluster` on a fresh host walks you through it: create a new cluster,
initialize skali on it with your domains, a Let's Encrypt account email, and
the first admin account. When it finishes it prints the console URL. The
whole thing takes a few minutes and is repeatable: run `sudo skali cluster`
again at any time to see status or open the maintenance menu.

Non-interactive installs pass `--config` files instead
(`skali cluster install --config node.yaml`, `skali cluster init --config
init.yaml`); the schemas for both are in [`schemas/`](schemas/).

### More nodes

```sh
# on the server: print a one-use join invitation
sudo skali cluster token

# on each new host, after installing the CLI
sudo skali cluster join skali.example.com --token-file ./invitation

# back on the server: review and converge the topology in one step
sudo skali cluster plan
sudo skali cluster apply --wait
```

Nodes declare capabilities (`application`, `database`, `edge`, ...) and
skali places workloads accordingly. Servers can be joined the same way for a
highly available control plane.

### Day two

```sh
sudo skali cluster status          # health of this node and the platform
sudo skali cluster diagnose        # find problems, with suggested fixes
sudo skali cluster repair          # apply them, each one confirmed
sudo skali cluster upgrade         # k3s and the platform, to this CLI's version
sudo skali cluster reset-password  # recover a locked-out admin account
```

## Deploy

Connect the CLI to your installation once, then deploy from any project
directory that has a `skali.yaml`:

```sh
skali remote add prod skali.example.com   # logs you in
cd my-project
skali deploy
```

The first deploy of a checkout asks which project and environment to target
and remembers the answer. Configuration values come from the environment's
stored values or a local env file (`--env-file .env.production`); every value
is stored encrypted, write-only, and shown by name only. Each deploy shows a
plan, builds locally, pushes into the platform's registry, and renders the
rollout live, ending with the URLs of your routes.

```sh
skali plan                        # what a deploy would change, without doing it
skali logs web                    # live logs of an application
skali exec web -- sh              # a shell in a running container
skali rollback                    # back to the previous revision
skali deploy --from staging       # promote staging's revision to this environment
skali backup create               # snapshot databases, buckets, and volumes
skali access set alice@example.com deploy   # roles: read, deploy, maintain, admin
```

The web console shows the same projects, deployments, logs, and settings in
the browser, and manages users.

## The manifest

```yaml
# yaml-language-server: $schema=https://skali.dev/schemas/v1/skali.schema.json
version: "1"
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

`${NAME}` references are per-environment values you provide; `{{ ... }}`
references are outputs of the databases and buckets skali provisions.
`skali validate` checks a manifest, and the schema line above gives you
completion and inline errors in any editor with YAML language support.

The [`examples/`](examples/) directory has complete projects: a minimal
build ([`hello-world`](examples/hello-world)), an imported image
([`whoami`](examples/whoami)), an app with a database, a bucket, and a
volume ([`guestbook`](examples/guestbook)), and the local hot-reload loop
([`dev-loop`](examples/dev-loop)).

If you write manifests with a coding agent, `skali skill install` gives it
the complete manifest reference and the platform's rules.

## Documentation

- [`docs/databases.md`](docs/databases.md): managed PostgreSQL, engines and
  versions, extensions, isolation and availability.
- [`docs/buckets.md`](docs/buckets.md): S3 buckets, quotas, endpoints, and
  topology.
- [`docs/storage.md`](docs/storage.md): persistent volumes and the storage
  drivers.
- [`docs/permissions.md`](docs/permissions.md): users, roles, environments,
  and protection.
- [`docs/build-matrix.md`](docs/build-matrix.md): supported Dockerfile and
  BuildKit features.
- [`docs/development.md`](docs/development.md): working on skali itself.
- [`ROADMAP.md`](ROADMAP.md): what exists and what comes next.

## How it fits together

`skali` is the CLI: it builds on your machine, talks to the platform's API,
and installs and maintains the servers. `skalid` is the control plane that
runs on the cluster: it stores what you declared, compiles it into
Kubernetes objects, and reports status back. The console is a web app served
on the platform domain. k3s and a small set of operators (CloudNativePG,
Traefik, cert-manager, SeaweedFS, optionally Longhorn) do the generic
orchestration; skali owns the hosts it runs on and never adopts a cluster it
did not install.

## License

skali is released under the [Apache License 2.0](LICENSE). You can run it
for yourself, for your company, or for your own customers, modify it, and
redistribute it; see the license for the full terms. Security reports go
through [SECURITY.md](SECURITY.md).
