# Supported BuildKit feature matrix (R3)

The local build executor drives BuildKit through `docker buildx build`
(the R0-resolved local builder). The manifest describes how to build; the
engine guarantees the same artifact contract regardless of executor. This
matrix documents what the R3 build schema and engine support.

## Supported

| Feature | Manifest surface | Engine behavior |
| --- | --- | --- |
| Dockerfile builds | `build.context`, `build.dockerfile` | The context is project-root-relative; the Dockerfile is context-relative (docker convention, default `Dockerfile`). Neither may escape the project root; the Dockerfile may live outside the context directory via `..`. |
| Multi-stage targets | `build.target` | Passed as `--target`; part of the build configuration hash. |
| Build arguments | `build.arguments` | Passed as `--build-arg` verbatim (literal strings, no `${NAME}` interpolation). Part of the configuration hash. |
| Target platform | `platforms` (application level) | Derived per application: the declared platforms intersect with the platforms observed on the cluster's application nodes; a cluster platform preference picks a single arch, otherwise all candidates build as one multi-platform image. Falls back to `linux/<host arch>` when nothing is known. Recorded on the build, part of the input hash, and rendered as node arch affinity on the workload. `--platform` overrides. |
| Ignore rules | `.dockerignore` in the context root | dockerignore pattern syntax, including `!` exceptions. Patterns are rooted: use `**/*.log` to reach subdirectories. |
| Layer caching | (automatic) | The engine uses the local BuildKit cache; an unchanged input hash skips the build entirely and reuses the verified artifact. |
| Push and digest capture | (automatic) | `--push` with `--metadata-file`; attestations are disabled (`--provenance=false --sbom=false`) so the pushed digest is the plain manifest digest, verified server-side before the artifact enters a revision. |

## Enforced safety rules

- Build arguments persist in image configuration and history, so never
  put credentials in `build.arguments`. They are literal manifest text;
  project values never feed a build. The engine supports BuildKit
  secret mounts (`--secret id=...,env=...`) for the future secret-input
  surface; those values travel only through the child process
  environment, never argv, image history, or any hash.
- `.git/`, `.hg/`, `.svn/`, `.skali/`, `.env`, `.env.*`, and the selected
  environment file are always excluded from the context and its hash.
- Symlinks must stay inside the context; absolute or escaping targets fail
  collection.

## Not supported in R3

- Cloud builders and build-context upload (R4; the schema and artifact
  contract are shared).
- Buildpacks and Nixpacks (a future engine behind the same interface).
- Remote cache export/import (`--cache-to`/`--cache-from`).
- Named build contexts, SSH forwarding, and Dockerfile syntax directives
  requiring a custom frontend.
