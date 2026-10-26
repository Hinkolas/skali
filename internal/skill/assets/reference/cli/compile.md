# skali compile

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli compile`.
[CLI overview](../cli.md). [Workflow guide](workflows/deployments.md): `skali skill read cli workflows deployments`.

```text
skali compile [flags]
```

Compile a Skali manifest into canonical IR, a revision, or Kubernetes objects

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--env-file` | `string` | `""` | dotenv file used to resolve project variables for revision or kubernetes output |
| `--environment` | `string` | `local` | environment name recorded in revision output |
| `--help`, `-h` | `bool` | `false` | help for compile |
| `--image` | `stringArray` | `[]` | prepared image for a build application, as service=reference |
| `--manifest` | `string` | `""` | manifest path; defaults to skali.yml or skali.yaml |
| `--namespace` | `string` | `""` | Kubernetes namespace used for rendering |
| `--offline` | `bool` | `false` | use the recorded target release and an available CLI without contacting the remote or downloading |
| `--remote` | `string` | `""` | target remote, overriding the checkout binding and current remote |
| `--target` | `string` | `definition` | compile target: definition, revision, or kubernetes |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
