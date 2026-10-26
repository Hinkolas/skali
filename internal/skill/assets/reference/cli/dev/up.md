# skali dev up

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev up`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/development.md): `skali skill read cli workflows development`.

```text
skali dev up [flags]
```

Build, deploy, and attach to the current project (same as bare skali dev)

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--detach`, `-d` | `bool` | `false` | exit once the rollout settles instead of following runtime logs |
| `--env-file` | `string` | `""` | explicit local env file for this run only (default: the choice remembered for local, else the lone .env/.env.* file, else a one-time pick) |
| `--force` | `bool` | `false` | deploy even when nothing changed, cancelling any in-flight run first; application workloads are restarted (data is untouched) |
| `--help`, `-h` | `bool` | `false` | help for up |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--pick-env-file` | `bool` | `false` | ask which env file (or the stored values) to use and remember the answer |
| `--platform` | `string` | `""` | override the build platform(s), e.g. linux/amd64 or a comma list (default: the cluster architecture) |
| `--preview` | `bool` | `false` | build and deploy every application in the cluster, ignoring dev blocks; no local dev processes run |
| `--prune-values` | `bool` | `false` | remove stored values the manifest no longer references as part of this deployment |
| `--rebuild` | `bool` | `false` | rebuild and re-import artifacts without caches, picking up moved base images (implies --force) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
