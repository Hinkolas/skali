# 06 — Web UI & CLI

> [!WARNING]
> Historical first-rework document, superseded by
> [`REWORK_V2.md`](../REWORK_V2.md). Retained for context; this client scope and
> delivery sequence are not the current V2 contract.

## The web survives whole — the mock gets promoted

The mock services prototype (`web/src/lib/mock/`) was the design spec all along; the
rework makes it real. Base UX, component library, shell, auth flows: untouched.

### Mock → real mapping

| Mock (today) | Real (after rework) |
| --- | --- |
| `mock/types.ts` `Service` union | `lib/types/services.ts` mirroring the Go API (snake_case, as conventions doc says) |
| `mock/index.ts` `listProjects/listServices/getService` | `+page.server.ts` loads via `apiFetch` → `/v1/projects...` |
| `DatabaseService.connection` object | `GET .../services/{slug}/connection` (password fetched on reveal, not in page data) |
| `ApplicationService.deployments` | `GET .../services/{slug}/releases` |
| `NewServiceModal` types list | `application`, `database` live; `cache`, `object_storage`, `static_site` disabled "coming soon" |
| `connected_apps` / `connected_service_slugs` | `connections` rows |

Existing patterns to reuse verbatim: poll-while-converging (workloads page), sudo
gating after modal close, `dialog.confirm` for destructive actions (delete database
confirmation must name the data loss), `CopyField` masked mode for passwords/URLs,
`SERVICE_TABS` + `SidebarServiceNav` for detail-page tabs, `TimeSeriesChart` for
analytics later.

### Navigation changes

- **Projects → services** becomes the primary product surface (routes already exist:
  `projects/[project]/services/[service]/[tab]`).
- Detail tabs v1: **Overview** (+ **Logs** for apps, **Settings** for both). Analytics,
  Backups, Networking, Studio stay `stub: true` until their milestone.
- Administration section: **Workloads page is deleted** (concept gone). **Containers
  and Registry pages are deleted** (no docker engine surface, no mirror). **Nodes
  page stays**, rebuilt read-only on `/v1/nodes` (kube-backed: k3s version, roles,
  capacity/usage from metrics-server) with the add-node join-command modal.
- New-service create pages follow the Sevalla create form (type picker with coming-
  soon tiles, generated names, engine/version for DBs, image/resources for apps).

### Deletions in `web/`

`routes/(app)/workloads/`, `routes/(app)/containers/`, `routes/(app)/registry/`,
`components/workloads/`, `lib/types/workloads.ts`, workload/container/registry
entries in `service-types.ts` + `navigation.ts`, and finally `lib/mock/` itself once
the real API covers the pages.

## CLI (`cmd/skali`)

Concept kept: a thin client of the skali API (token auth via `internal/cliconfig`),
never a kubectl wrapper. v1 scope (M4, after the API settles):

```
skali login
skali projects list / create
skali services list [-p project]
skali deploy <project>/<service> [--image tag]     # the CI-friendly verb
skali logs <project>/<service> [-f]
skali status <project>/<service>
```

Design rule: every CLI verb is a 1:1 call to a public API endpoint — no CLI-only
behavior. `kubectl` remains the documented admin escape hatch for anything below the
product layer.
