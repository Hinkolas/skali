# skali dev upgrade

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev upgrade`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/development.md): `skali skill read cli workflows development`.

```text
skali dev upgrade [flags]
```

Moves the existing local platform to this skali's release and k3s pin
without a reset: projects, environments, values, credentials, databases,
buckets, volumes, and the development CA (so its trust and every dev proxy
pinned to it) stay as they are. The plan (versions, component changes,
database migrations, and the expected interruption) is shown before
anything changes and needs confirmation or --yes. Releases only move
forward and k3s one minor version at a time; anything else is refused
before it starts. An interrupted or failed upgrade keeps all data;
running skali dev upgrade again continues it.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--help`, `-h` | `bool` | `false` | help for upgrade |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
| `--yes` | `bool` | `false` | apply the plan without asking |
