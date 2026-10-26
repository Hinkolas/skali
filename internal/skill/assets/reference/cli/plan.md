# skali plan

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli plan`.
[CLI overview](../cli.md). [Workflow guide](workflows/deployments.md): `skali skill read cli workflows deployments`.

```text
skali plan [flags]
```

Compiles and submits the candidate definition, then prints the server's
plan without building images, uploading env-file values, or changing the
environment's active revision. Local review and env-file selection may be
recorded. --env-file is validated locally; the plan uses stored values.
With --from, previews promotion of the source's active revision instead.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--bypass-protection` | `bool` | `false` | deploy into a promote-only environment anyway (environment admins with a recent login; recorded on the run) |
| `--env-file` | `string` | `""` | dotenv file to stage as candidate values for this run only (default: remembered choice; with --from: stored target values; plan only validates the file) |
| `--environment` | `string` | `""` | target environment name (defaults to the checkout binding, prompted interactively otherwise) |
| `--from` | `string` | `""` | promote the active revision of another environment of this project; nothing builds and no manifest is read |
| `--help`, `-h` | `bool` | `false` | help for plan |
| `--manifest` | `string` | `""` | explicit manifest path (otherwise discover skali.yaml or skali.yml up from the working directory) |
| `--pick-env-file` | `bool` | `false` | ask which env file (or the stored values) to use and remember the answer for the environment (not used with --from) |
| `--platform` | `string` | `""` | override the build platform(s), e.g. linux/amd64 or a comma list (default: application platforms intersected with cluster platforms and preference) |
| `--prune-values` | `bool` | `false` | remove stored values the manifest no longer references as part of this deployment |
| `--rebuild` | `bool` | `false` | plan as if artifacts were rebuilt and re-imported without reuse |
| `--remote` | `string` | `""` | remote to target for this one invocation, ignoring the checkout binding and the current remote |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
