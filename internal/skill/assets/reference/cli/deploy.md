# skali deploy

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli deploy`.
[CLI overview](../cli.md). [Workflow guide](workflows/deployments.md): `skali skill read cli workflows deployments`.

```text
skali deploy [flags]
```

Deploys the local manifest: candidate definition, staged values, plan
confirmation, local builds and imports pushed to the managed registry,
server-side verification, and the journaled rollout, rendered live.
Values come from --env-file, else the env file (or stored values)
remembered for the environment in .skali/, else a one-time pick among
the checkout's .env/.env.* files that is remembered from then on;
--pick-env-file asks again. Without env files the stored values apply.

Builds and pushes need this terminal, so Ctrl-C during them (or
closing the terminal) cancels the deployment. Once they are done the
run continues on the server and the live view says so: d detaches
and leaves it running, Ctrl-C pressed twice cancels it. --detach
returns as soon as the run is handed over.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--allow-destructive` | `bool` | `false` | approve a destructive plan (non-interactive) |
| `--build` | `string` | `auto` | build executor: local or auto (auto follows installation policy; local today) |
| `--bypass-protection` | `bool` | `false` | deploy into a promote-only environment anyway (environment admins with a recent login; recorded on the run) |
| `--detach` | `bool` | `false` | return after builds and uploads hand the run to the server, without waiting for the rollout |
| `--env-file` | `string` | `""` | dotenv file to stage as candidate values for this run only (default: remembered choice; with --from: stored target values; plan only validates the file) |
| `--environment` | `string` | `""` | target environment name (defaults to the checkout binding, prompted interactively otherwise) |
| `--force` | `bool` | `false` | deploy even when nothing changed; application workloads are restarted (data is untouched) |
| `--from` | `string` | `""` | promote the active revision of another environment of this project; nothing builds and no manifest is read |
| `--help`, `-h` | `bool` | `false` | help for deploy |
| `--manifest` | `string` | `""` | explicit manifest path (otherwise discover skali.yaml or skali.yml up from the working directory) |
| `--pick-env-file` | `bool` | `false` | ask which env file (or the stored values) to use and remember the answer for the environment (not used with --from) |
| `--platform` | `string` | `""` | override the build platform(s), e.g. linux/amd64 or a comma list (default: application platforms intersected with cluster platforms and preference) |
| `--prune-values` | `bool` | `false` | remove stored values the manifest no longer references as part of this deployment |
| `--rebuild` | `bool` | `false` | rebuild and re-import artifacts without caches, picking up moved base images (implies --force) |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | approve a non-destructive plan without prompting |
