# Local development

Read with `skali skill read cli workflows development`.
[CLI overview and command index](../../cli.md).

- `skali dev` is the paved path: it ensures the disposable local platform
  and deploys the project. Applications with a `dev:` block are not built;
  their dev command runs on this machine with the real resolved
  environment, and the cluster's routes reach the host process on
  auto-allocated ports (injected as `SKALI_PORT_<NAME>` and `PORT`,
  usable as `${PORT}` in the dev command). Ctrl-C pauses the project;
  `d` detaches (host dev processes still stop).
- `skali dev --preview` deploys everything in the cluster, exactly like a
  remote deploy would, ignoring dev blocks.
- `skali dev -d` waits for rollout then leaves container workloads running.
  It refuses applications with dev blocks because host processes cannot
  outlive the CLI; combine
  `--preview -d` for a fully containerized background preview. `dev down`
  stops the project's workloads and retains data, while `dev down --purge`
  destroys that local environment. `dev reset` destroys the entire local
  platform, including other projects. These scopes are different from
  `dev stop`, which stops the platform while retaining its data.
- One fixed `skali-dev` platform uses the selected target's release. An absent
  platform is created; a matching platform is reused. An older release or k3s
  pin fails before changing it and names `skali dev upgrade`, which moves the
  platform forward in place after showing its plan and asking for confirmation
  (`--yes` skips it). Projects, data, credentials, and the development CA are
  kept. An interrupted upgrade blocks the platform until `skali dev upgrade`
  runs again and finishes it. A newer release, a k3s jump of more than one
  minor version, and working-tree/release transitions require `skali dev
  reset`, which discards the data. `skali dev --remote <name>` selects another
  target.
- `skali dev stop` retains data; `skali dev start` restarts it. Stop, reset and
  platform status work without the remote or a CLI download. Status identifies
  the installed release and an unfinished upgrade. Stop distinguishes stopped,
  already stopped, and absent clusters. Confirmed reset can delete the fixed
  cluster even if its installation record is missing, and also removes a
  partial cluster (a leftover helper container, volume or network) that k3d
  cluster list no longer shows. There is no `dev prune`.
- `skali dev run <name>` runs a named command from the manifest
  (`applications.<app>.commands`) on this machine with the application's
  resolved environment; `skali dev run <app> -- <command>...` runs a raw
  command; bare `skali dev run` lists the declared commands. Database and
  bucket addresses are rewritten to the local platform's loopback ports,
  so seeds and migrations just work.
- `skali dev exec [app]` opens a shell inside the running container
  instead; `skali run` manages journal runs, not project commands.
- `skali dev sql [database]` runs SQL against a database of the local
  platform, exactly like `skali sql` (see [Databases, SQL, and container exec](databases.md)) without the target
  flags. It needs no running application and no local psql, so it keeps
  working after `skali dev down`.
- When `skali dev` runs on a remote machine and the browser on a laptop,
  `skali dev proxy <host> --ca sha256:…` on the laptop opens the remote
  platform at the same `https://*.localhost` origins. `skali dev expose
  <address>` on the remote machine opens its edge to the proxy, and `skali dev
  status` there prints the exact proxy command with the CA fingerprint.
