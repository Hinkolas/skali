# skali cluster join

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster join`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

```text
skali cluster join [coordinator] [flags]
```

Join this host to an existing cluster

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--capabilities` | `stringSlice` | `[]` | designated workload capabilities for this node |
| `--cluster` | `string` | `""` | expected cluster name; required only for raw legacy K10 tokens |
| `--coordinator-bind` | `stringSlice` | `[]` | scopes the enrollment coordinator listens on: cluster, public, or both (servers only) |
| `--help`, `-h` | `bool` | `false` | help for join |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--node-ip` | `string` | `""` | address other cluster nodes reach this node through; defaults to the coordinator route source when unambiguous |
| `--public-ip` | `stringSlice` | `[]` | address reachable from outside the cluster network; repeatable |
| `--role` | `string` | `""` | expected role; must match the invitation or legacy token |
| `--server` | `string` | `""` | coordinator endpoint; legacy composite tokens may supply their k3s endpoint |
| `--tls-san` | `stringSlice` | `[]` | additional name or address for the kubernetes api certificate; repeatable |
| `--token` | `string` | `""` | join token (defaults to SKALI_JOIN_TOKEN or saved enrollment) |
| `--token-file` | `string` | `""` | path to a join token file, or - for stdin |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
| `--yes` | `bool` | `false` | provision missing Mac dependencies without confirmation (macOS only) |
