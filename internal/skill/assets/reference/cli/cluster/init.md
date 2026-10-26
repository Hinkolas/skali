# skali cluster init

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli cluster init`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/cluster.md): `skali skill read cli workflows cluster`.

```text
skali cluster init [flags]
```

Initialize Skali on the joined cluster (run once on a server)

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--config` | `string` | `""` | non-interactive init configuration (init.yaml) |
| `--help`, `-h` | `bool` | `false` | help for init |
| `--hostd-bin` | `string` | `""` | Linux skali-hostd binary to install on managed nodes (source installs) (inherited) |
| `--image-tar` | `string` | `""` | docker-save tar of the skalid image, imported into the node during init (source installs) (inherited) |
| `--layout` | `string` | `""` | cluster-layout document asserting the expected membership |
| `--platform-preference` | `string` | `""` | ordered build platform preference for mixed-architecture clusters, comma-separated (for example linux/arm64); empty keeps the recorded choice |
| `--storage-driver` | `string` | `""` | application storage driver: local (default) or longhorn; re-run init with longhorn to enable it on an existing cluster |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--vm` | `string` | `skali` | name of the Lima VM hosting the skali node (macOS only) (inherited) |
