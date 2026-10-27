# skali cluster addresses

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster addresses`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

```text
skali cluster addresses [flags]
```

Declare how this host is addressed inside and outside the cluster

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--cluster-ip` | `string` | `""` | address other cluster nodes reach this node through |
| `--coordinator-bind` | `stringSlice` | `[]` | scopes the enrollment coordinator listens on: cluster, public, or both |
| `--help`, `-h` | `bool` | `false` | help for addresses |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--public-ip` | `stringSlice` | `[]` | address reachable from outside the cluster network; repeatable |
| `--tls-san` | `stringSlice` | `[]` | additional name or address for the kubernetes api certificate; repeatable |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | record the declaration without confirmation |
