# skali dev

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev`.
[CLI overview](../cli.md). [Workflow guide](workflows/development.md): `skali skill read cli workflows development`.

```text
skali dev [flags]
```

Bare skali dev (shorthand for skali dev up) is the complete paved
path: it ensures the disposable local platform (k3d cluster with
in-cluster skalid, Postgres, and registry), builds and deploys the
current project, attaches to the rollout, and follows the runtime
logs. Like docker compose, ending the session (Ctrl-C, closing the
terminal) pauses the project; its data is retained and the next
skali dev brings it back. Pressing d while the logs follow detaches
instead: the session ends and the project keeps running, as if
started with -d. A rollout already in flight is adopted: dev
attaches to it instead of failing; --force cancels it and
redeploys. Use -d for a background project that keeps running,
skali dev down to pause it explicitly, and skali dev list to see
everything on the local platform. The platform runs the same
Skali release as the project's target cluster (the checkout
binding, else the current remote, else this skali), using that release
in one local cluster; skali dev upgrade moves it to a newer release in
place. The platform lifecycle lives under skali dev start, stop,
upgrade, and reset. Values come from the env file remembered for the local
environment in .skali/ (a lone .env/.env.* file is taken as is, several
are picked from once); --pick-env-file asks again. Local values never
leave this machine.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach`, `-d` | `bool` | `false` | exit once the rollout settles instead of following runtime logs |
| `--env-file` | `string` | `""` | explicit local env file for this run only (default: the choice remembered for local, else the lone .env/.env.* file, else a one-time pick) |
| `--force` | `bool` | `false` | deploy even when nothing changed, cancelling any in-flight run first; application workloads are restarted (data is untouched) |
| `--help`, `-h` | `bool` | `false` | help for dev |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI |
| `--pick-env-file` | `bool` | `false` | ask which env file (or the stored values) to use and remember the answer |
| `--platform` | `string` | `""` | override the build platform(s), e.g. linux/amd64 or a comma list (default: the cluster architecture) |
| `--preview` | `bool` | `false` | build and deploy every application in the cluster, ignoring dev blocks; no local dev processes run |
| `--prune-values` | `bool` | `false` | remove stored values the manifest no longer references as part of this deployment |
| `--rebuild` | `bool` | `false` | rebuild and re-import artifacts without caches, picking up moved base images (implies --force) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |

## Subcommands

- [`skali dev down`](dev/down.md): Remove the project from the local platform; data is retained
- [`skali dev exec`](dev/exec.md): Run a command in a running app container
- [`skali dev expose`](dev/expose.md): Let skali dev proxy on another machine reach this machine's local platform
- [`skali dev list`](dev/list.md): List projects on the local platform
- [`skali dev logs`](dev/logs.md): Stream the local project's runtime logs
- [`skali dev proxy`](dev/proxy.md): Open a skali dev platform on another machine at its *.localhost origins
- [`skali dev reset`](dev/reset.md): Destroy the local dev platform completely
- [`skali dev run`](dev/run.md): Run a command on the host with an application's resolved environment
- [`skali dev sql`](dev/sql.md): Run SQL against a database of the local platform
- [`skali dev start`](dev/start.md): Start the local platform, creating it when absent
- [`skali dev status`](dev/status.md): Show platform and current-project state
- [`skali dev stop`](dev/stop.md): Stop the running local platform; state is retained
- [`skali dev trust`](dev/trust.md): Trust a skali dev platform's development CA in this machine's browsers
- [`skali dev up`](dev/up.md): Build, deploy, and attach to the current project (same as bare skali dev)
- [`skali dev upgrade`](dev/upgrade.md): Upgrade the local platform in place, keeping its data and development CA
- [`skali dev values`](dev/values.md): List the local project's stored values by name and version
