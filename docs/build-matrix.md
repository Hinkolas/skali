# Supported BuildKit feature matrix (R3)

The local build executor drives BuildKit through `docker buildx build`
(the R0-resolved local builder). The manifest describes how to build; the
engine guarantees the same artifact contract regardless of executor. This
matrix documents what the R3 build schema and engine support.

## Supported

| Feature | Manifest surface | Engine behavior |
| --- | --- | --- |
| Dockerfile builds | `build.context`, `build.dockerfile` | Paths are project-root-relative and cannot escape the root; the Dockerfile may live outside the context directory. |
| Multi-stage targets | `build.target` | Passed as `--target`; part of the build configuration hash. |
| Plain build arguments | `build.arguments` | Passed as `--build-arg`; values may reference non-secret project values (`${NAME}`). Part of the configuration hash. |
| Target platform | (derived) | `linux/<host arch>` in R3; recorded on the build and part of the input hash. Cross-platform selection is an R4 surface. |
| Ignore rules | `.dockerignore` in the context root | dockerignore pattern syntax, including `!` exceptions. Patterns are rooted: use `**/*.log` to reach subdirectories. |
| Layer caching | (automatic) | The engine uses the local BuildKit cache; an unchanged input hash skips the build entirely and reuses the verified artifact. |
| Push and digest capture | (automatic) | `--push` with `--metadata-file`; attestations are disabled (`--provenance=false --sbom=false`) so the pushed digest is the plain manifest digest, verified server-side before the artifact enters a revision. |

## Enforced safety rules

- Secret project values can never reach `build.arguments`; the compiler
  rejects the reference. The engine supports BuildKit secret mounts
  (`--secret id=...,env=...`) for the future secret-input surface; values
  travel only through the child process environment, never argv, image
  history, or any hash.
- `.git/`, `.hg/`, `.svn/`, `.skali/`, `.env`, `.env.*`, and the selected
  environment file are always excluded from the context and its hash.
- Symlinks must stay inside the context; absolute or escaping targets fail
  collection.

## Not supported in R3

- Cloud builders and build-context upload (R4; the schema and artifact
  contract are shared).
- Buildpacks and Nixpacks (a future engine behind the same interface).
- Explicit cross-platform builds and multi-platform release artifacts.
- Remote cache export/import (`--cache-to`/`--cache-from`).
- Named build contexts, SSH forwarding, and Dockerfile syntax directives
  requiring a custom frontend.
