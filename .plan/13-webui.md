# 13 — Web UI

## Stack & embedding

- **SvelteKit** with **`adapter-static`** → compiled to static assets.
- Embedded into `skalid` via **`go:embed`** and served by the master's REST
  server (e.g. mounted at `/` with the API under `/api/v1`).
- **No Node.js runtime on the server.** The UI is a pure SPA/static site; all
  data access is the same REST API the CLI uses (`06`). This is the architectural
  reason Drizzle/Node-side DB has no place here — Go owns the DB entirely (`05`).

## Build integration

- `web/` is the SvelteKit project. A build step (`14`) runs `vite build` →
  `web/build/`, which `skalid` embeds at Go build time.
- Dev workflow: run SvelteKit dev server against a running `skalid` API
  (CORS allowed for the dev origin); production embeds the static output.
- SPA fallback: adapter-static with a fallback (e.g. `index.html` / `200.html`)
  so client-side routing works for deep links; API routes are served before the
  static handler.

## Auth in the browser

- The web UI authenticates with the **same opaque token** system (`08`),
  delivered as an **httpOnly, Secure, SameSite cookie** (not localStorage) so the
  token isn't script-reachable. CSRF protection on state-changing requests.
- Login form → `POST /api/v1/auth/login` → cookie set → subsequent calls
  authenticated by cookie.

## Feature surface (mirrors the API)

- **0.1.0:** auth/login; **bundle** (project) list + detail showing its
  applications, databases, and volumes; trigger/observe deploys with **live build
  logs (SSE)**; runtime log tail (SSE); env management; routes/domain management;
  **managed databases** (create, connection string, backups + restore);
  **volumes** (create, size limit, usage); **bindings** (wire app⟷resource); nodes
  list + per-node storage capacity/headroom; registries.
- **[soon]:** rollback UI, replica/scaling controls, build history, RBAC user
  management, metrics dashboards, allocation/move controls for databases.
- **[future]:** **connect external object storage** (S3/R2/MinIO) and surface it
  in the UI — the deferred storage convenience (`16`/`17`); it is a *connected
  backend* shown here, not a provisioned runtime resource.

## Streaming in the browser

- Build logs and runtime logs use **SSE** (`EventSource`) — trivial in the
  browser, matches the CLI's transport. No WebSocket needed for one-way streams.

## Why this matters for the architecture

Keeping the UI static + embedded means the entire product ships as **two Go
binaries** with no runtime dependencies — the daemon serves its own UI and API.
This is what makes "install skalid, get a working node with a web console" true.
