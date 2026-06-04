# 06 — REST API (client plane)

The REST API is how the **CLI and web UI** talk to the master. It is **not** the
node-to-node plane (that's gRPC, see `07`). Built on **chi**.

## Conventions

- Base path **`/api/v1`** (explicit versioning from the start).
- JSON request/response; `application/json`. Build-context upload is
  `multipart/form-data`.
- Auth: `Authorization: Bearer <token>` (CLI) or an httpOnly secure cookie
  carrying the same token (web). See `08`.
- Errors: consistent envelope
  `{ "error": { "code": "string", "message": "human", "details": {…} } }`
  with appropriate HTTP status.
- Pagination: cursor-based (`?limit=&cursor=`) returning `{ items, next_cursor }`.
- Idempotency: deploy creation returns a stable deploy/build id; clients may
  resume log streams against it.
- **Streaming uses SSE** (`text/event-stream`) — simple, one-way, plain HTTP,
  trivial in Go. Used for build logs and runtime log tailing. (WebSocket only if
  we later need bidirectional; not for 0.1.0.)

## Resources & endpoints

### Auth

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/auth/login` | Credentials → mints a token (CLI: returns token; web: sets cookie) |
| `POST` | `/auth/logout` | Revoke the current token |
| `GET`  | `/auth/whoami` | Current user + token info |
| `GET`  | `/auth/tokens` / `POST` / `DELETE /auth/tokens/{id}` | Manage tokens/devices |

`[future]` `POST /auth/device/start` + `/auth/device/poll` for the GitHub-CLI /
Claude-Code-style browser login (mints the *same* opaque token).

### Projects

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/projects` | List |
| `POST` | `/projects` | Create (name, registry, build settings) + its default `production` environment |
| `GET` | `/projects/{id}` | Detail (environments, current releases, status) |
| `PATCH` | `/projects/{id}` | Update project-level settings (registry, build_per_env) |
| `DELETE` | `/projects/{id}` | Remove project + all environments + tear down instances |

### Environments

Routes, env vars, releases, replicas, placement, and resource limits all hang off
an **environment** (`05`). Most project-scoped routes below accept `?env=<name>`
(default: the project's default environment, `production`).

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/projects/{id}/apply` | **Declarative reconcile**: submit the project's env topology (from `config.yaml`); creates missing envs, adds declared routes, drift-corrects declared fields. Additive by default; `?prune=true` deletes file-absent resources. The `skali apply`/`deploy` target. |
| `GET` | `/projects/{id}/environments` | List environments + their routes/current release |
| `POST` | `/projects/{id}/environments` | Create an env (name, routes, resources, placement) — the granular form of apply |
| `GET` | `/projects/{id}/environments/{env}` | Detail |
| `PATCH` | `/projects/{id}/environments/{env}` | Update replicas/placement/egress/resources |
| `DELETE` | `/projects/{id}/environments/{env}` | Remove env + tear down its instances |
| `POST` | `/projects/{id}/environments/{env}/promote?to={target}` | `[soon]` Reuse this env's current digest → new Release in `{target}` (no rebuild; copies blob if the target's registry differs) |

`POST /apply` never touches env var **values** (those sync via the env endpoints
below); it reconciles **topology only**, so a config push can't leak or clobber
secrets.

### Deploys / builds / releases

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/projects/{id}/deploys?env={name}` | Start a deploy into `{env}`. **Returns immediately** (async) with `{ deploy_id, build_id? }`. **Two body modes:** *multipart* (upload build context → a builder node builds+pushes), or *JSON* `{ image_digest }` (a **locally-built** image already pushed → release-only, no upload). |
| `GET` | `/builds` | List active + recent build/deploy **jobs** (for `skali builds ls`); `?status=active`, `?project=` |
| `GET` | `/deploys/{id}` | Job status (queued/building/pushing/rolling-out/done/failed) |
| `GET` | `/deploys/{id}/logs` | **SSE** stream of build + rollout logs (attach for `--follow` / `skali builds logs`) |
| `POST` | `/deploys/{id}/cancel` | `[soon]` Cancel a queued/running job |
| `GET` | `/projects/{id}/releases?env={name}` | List a single env's releases (for rollback UI) |
| `POST` | `/projects/{id}/releases/{rid}/rollback` | Set a prior release as its env's current |
| `POST` | `/projects/{id}/redeploy?env={name}` | New Release from `{env}`'s **current image** + latest env/config; rolls out (applies env changes, no rebuild) |
| `POST` | `/projects/{id}/restart?env={name}` | Bounce `{env}`'s current Release's instances (no new Release) |

The JSON `{ image_digest }` mode (0.1.0) powers **local build** (`04`): the CLI
pushes its locally-built image, then releases that digest. `[soon]` extends the
same mode to an **arbitrary external image** (`{ "image": "nginx:1.27" }`) for
"run any image" deploys.

A local-build deploy also needs a registry push credential: `POST
/registries/push-token?project={id}` (`[soon]`/0.1.0) mints a **short-lived,
scoped** token the CLI uses to `docker push` to the built-in registry (`04`).

### Env / secrets (per environment)

Variables are **keyed by environment**; all of these take `?env={name}` (default
`production`).

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/projects/{id}/env?env={name}` | List keys for the env (values masked) |
| `PUT` | `/projects/{id}/env?env={name}` | Bulk set (the `skali env vars push` target: add freely, caller resolves overwrites) |
| `DELETE` | `/projects/{id}/env/{key}?env={name}` | Remove one |

`PUT /env` and `DELETE /env/{key}` **store only — they do not roll out.** Env is
snapshotted into a Release, so staged changes apply on the next deploy or via an
explicit `POST /projects/{id}/redeploy?env=` (or `…/restart`). This avoids surprise
restarts.

### Routes / domains (per environment)

Host rules belong to an environment (so `staging.example.com` and `example.com`
are routes of different envs). All take `?env={name}` (default `production`).

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/projects/{id}/routes?env={name}` | List host rules |
| `POST` | `/projects/{id}/routes?env={name}` | Add a domain/host rule |
| `DELETE` | `/projects/{id}/routes/{rid}` | Remove |

`[soon]` A route/environment may carry an **access policy** (basic-auth; `[future]`
IP allowlist / forward-auth) set via the route/environment PATCH and rendered as a
Traefik middleware — e.g. to keep a staging site private. See `02`/`11`.

### Runtime logs & status

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/projects/{id}/logs?env={name}` | **SSE** tail of aggregated container logs (one env) |
| `GET` | `/projects/{id}/instances?env={name}` | Running instances + health, per node (one env) |

### Nodes & cluster (admin)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/nodes` | List nodes, roles, status |
| `POST` | `/nodes/join-token` | Mint a one-time enrollment token (see `08`) |
| `PATCH` | `/nodes/{id}` | Edit roles / disable |
| `DELETE` | `/nodes/{id}` | Remove (revokes its cert) |

### Registries

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/registries` / `POST` | List / connect a registry |
| `DELETE` | `/registries/{id}` | Disconnect |

### Notifications (outgoing) `[future]`

A **unified notification system**: skali events route to subscriptions, each bound
to a **transport** — `webhook` (HMAC-signed, GitHub-style), `email` (cluster SMTP /
provider), `[future]` mobile push. One event core + outbox delivery engine; only
the send step is transport-specific. Event model + outbox design in `15`/`16`.

| Method | Path | Purpose |
|--------|------|---------|
| `GET` / `POST` | `/notifications/subscriptions` | List / create (events filter, scope, **transport**, target, secret) |
| `PATCH` / `DELETE` | `/notifications/subscriptions/{id}` | Update / remove |
| `POST` | `/notifications/subscriptions/{id}/test` | Send a test event |
| `GET` | `/notifications/subscriptions/{id}/deliveries` | Delivery log (status, attempts, response) |
| `GET` / `PUT` | `/notifications/settings` | Cluster transport settings (e.g. SMTP / email provider) |

### Internal (not for clients)

- `GET /internal/traefik/http?node={id}` — Traefik **HTTP-provider** endpoint
  rendering edge dynamic config from SQLite (remote-apps-only; see `02`). Bound
  to the private interface / authenticated separately from user tokens.

## Middleware stack (chi)

`RequestID → RealIP → Logger → Recoverer → CORS(webui) → Auth(bearer/cookie) →
RBAC → route`. Build-context upload route gets a size limit + streaming
multipart reader (don't buffer whole tarballs in memory).
