# skali dev start

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev start`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/development.md): `skali skill read cli workflows development`.

```text
skali dev start [flags]
```

Boots the local platform: creates it when absent, starts it when
stopped, and leaves a running one untouched beyond a health check.
Project data survives a stop/start cycle. --force runs a full
converge pass (re-applying the complete platform bundle) even when
the platform looks healthy; it is the repair verb for a platform
whose recorded state lies. The platform's release is this skali's;
an older release or k3s pin needs skali dev upgrade first.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--force` | `bool` | `false` | force a full converge pass even when the platform looks healthy |
| `--help`, `-h` | `bool` | `false` | help for start |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
