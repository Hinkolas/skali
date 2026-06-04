# 14 — Repo Layout & Tooling

## Monorepo structure

```
skali/                        # one Go module
├── go.mod
├── cmd/
│   ├── skali/                # CLI client (cobra + huh) → binary `skali`
│   └── skalid/               # daemon (cobra) → binary `skalid`
├── internal/
│   ├── service/              # domain logic: projects, builds, releases, auth
│   ├── orchestrator/         # reconciler (desired vs actual), rollout
│   ├── executor/             # Executor interface
│   │   ├── local/            # LocalExecutor (Docker Go SDK)
│   │   └── remote/           # RemoteExecutor (gRPC client)
│   ├── store/                # sqlc-generated code + repo helpers
│   ├── proxy/                # edge HTTP-provider rendering; container labels
│   ├── ca/                   # cluster CA: issue/sign/verify node certs
│   ├── auth/                 # tokens, password hashing, middleware
│   ├── registry/             # built-in registry management; registry auth
│   ├── transport/
│   │   ├── rest/             # chi handlers, SSE, middleware
│   │   └── grpc/             # gRPC server impl (wraps LocalExecutor)
│   ├── config/               # daemon + client config loading
│   └── crypto/               # [soon] secrets-at-rest (AEAD), master key handling
├── proto/                    # .proto + buf config; generated → gen/
│   └── skali/node/v1/
├── db/
│   ├── migrations/           # goose SQL migrations (embedded)
│   └── queries/              # sqlc query files
├── web/                      # SvelteKit (adapter-static); build → embedded
├── sqlc.yaml
├── buf.yaml / buf.gen.yaml
├── Makefile / Taskfile
└── .plan/                    # this plan
```

- `internal/` is shared by both binaries (the whole point of the monorepo).
- `cmd/skali` imports only client-appropriate packages (config, REST client,
  huh); it must **not** import Docker/gRPC-server/store packages.

## Codegen

| Tool | Input | Output |
|------|-------|--------|
| **sqlc** | `db/migrations` (schema) + `db/queries` | `internal/store` typed Go |
| **goose** | `db/migrations` | applied at runtime (`goose.Up`, embedded) |
| **buf** (or protoc) | `proto/**` | gRPC + message Go into `proto/gen` |
| **vite** | `web/` | static assets embedded via `go:embed` |

Generated Go is either checked in (simpler onboarding) or generated via
`make generate`. Pick one and document it; checking in sqlc/proto output avoids
requiring every contributor to install the toolchains.

## Build & dev

- `make build` → builds `skali` and `skalid` (CGO_DISABLED works thanks to
  `modernc.org/sqlite`, so easy cross-compilation for both).
- `make generate` → sqlc + buf + web build.
- `make dev` → run `skalid start` locally (single-node, all roles) against a
  local Docker; run SvelteKit dev server separately.
- **GoReleaser** (`[soon]`) for multi-arch release binaries + a `docker run`
  image for `skalid`.

## Dependencies (intended)

- CLI: `spf13/cobra`, `charmbracelet/huh`, an HTTP client.
- Daemon: `go-chi/chi`, `google.golang.org/grpc`, `docker/docker` (SDK),
  `pressly/goose`, `modernc.org/sqlite`, an AEAD lib (`golang.org/x/crypto`),
  argon2id, ULID/UUID.
- Codegen: `sqlc`, `buf`.

## Testing strategy

- Unit-test the `service`/`orchestrator` against a **fake `Executor`** (no Docker
  needed) — the seam makes the reconciler fully testable in memory.
- Integration tests against a real Docker daemon for `LocalExecutor`.
- gRPC tested by running a `RemoteExecutor` against an in-process server wrapping
  a fake/local executor.
- Migration tests: `goose.Up` on a temp DB in CI; sqlc queries vetted against the
  migrated schema.
