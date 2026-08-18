# Permissions

Status: design agreed 2026-08-19, not implemented yet. Once implemented this
file stays as the description of how access works; the "Open decisions"
section at the end disappears as they are settled.

## Why

People with access to one skali installation have very different trust
levels. The motivating team:

- The sysadmin sets up skali and the hosts and may do everything.
- A programmer works on customer projects but must not reach every project,
  and on the projects they work on they deploy to staging while production
  is locked or promote-only.
- A colleague without infra knowledge hosts small internal tools. They own
  their own projects and must not see production projects at all.

Principles:

- Few concepts: instance role, project membership, an access level per
  environment, environment settings. No permission matrices per action.
- The environment is the boundary. Everything with a blast radius or a
  secret (deployments, runs, logs, values, exec, credentials, backups,
  routes) is environment scoped, so access is granted per environment. The
  project is a thin container.
- Access (who may act) and policy (which kind of act is allowed for anyone)
  are separate axes. Protection applies to admins too; admins bypass it only
  explicitly and it is recorded.
- Deny by default, enforced server-side in one place. The console and CLI
  hide and explain, they never enforce.
- Roles are code, not data: the database stores which role or level a user
  has, never what it means.
- Nothing lives in `skali.yml`. The manifest is environment-agnostic and
  whoever can deploy could edit it, so protection, priority, and access are
  server-side settings.

## Instance layer

`users.role` stays `admin | member`.

- `admin`: everything. Implicit project admin of every project, `deploy` on
  every environment. Manages users, nodes, system, backup targets. Only
  instance admins create or raise environments to high priority.
- `member`: nothing until granted through project membership.

One extra per-user permission bit for members: `create_projects` (default
off, set by instance admins). Creating a project makes the creator that
project's admin. Instance admins always may create projects and also get an
explicit admin membership row on projects they create, so a later demotion
to member keeps their own projects.

Invariants kept from today: at least one instance admin must remain
(`ErrLastAdmin`), users cannot change their own role or delete themselves,
`skalid user set-role` is the lost-admin recovery path (`skalid user delete`
must go through `auth.DeleteUser` so it honors the last-admin guard; today it
bypasses it).

## Projects

Membership: `(project, user) -> role: admin | member`, plus for members a
default access level (see below). Non-members do not see the project:
lists are filtered, direct access answers 404, exactly like a project that
does not exist.

- Project admin: manages members and access cells, environment settings
  (member default, protection, lowering priority), deletes the project,
  deletes or tears down any environment. Has `deploy` on every environment
  of the project and cannot be restricted per environment; to restrict
  someone, make them a member.
- Member: no rights of their own; everything comes from environment access.

Project-level reads (name, environments you can see, definition history,
draft, backups list filtered by environment) need a membership row or
instance admin. Project-level writes that belong to deploying (submitting a
definition, writing the draft, registry push, artifact verify, build
heartbeat) need "deployer": `deploy` on at least one environment of the
project.

There is no "last project admin" invariant: instance admins are implicit
admins everywhere, so a project can end up with zero explicit admins.

## Environments

Access level per `(member, environment)`: `none | read | deploy`.

- `none`: the environment does not exist for this user. Not listed, direct
  access 404, its runs, deployments, and backups are filtered out of
  project-wide lists.
- `read`: status and health, deployments, revisions, runs and run logs
  (redacted), runtime logs, value names and versions, routes, database and
  bucket connection projections (no secrets), backups list, environment
  settings.
- `deploy`: `read` plus plan, deploy, promote into it, rollback, set and
  prune values, backup create, restore into it, run cancel, exec, resolved
  application environment, credential reveal, teardown and delete when the
  environment is normal priority. Deploy is the secret boundary: exec alone
  reveals every value.

Effective level for a member on an environment:

    cell(member, env)                            if an explicit cell exists
    min(member.default_access, env.member_access) otherwise

Both defaults are ceilings; the explicit cell is the exception in either
direction (grant `deploy` on a locked environment, or `none` on an open
one). Project admins and instance admins are always `deploy` and ignore all
three inputs.

Environment settings (server-side, edited by project admins unless noted):

- `member_access: none | read | deploy` (default depends on priority at
  creation, see below): the ceiling for members without an explicit cell.
- `deploy_policy: direct | promote-only` (default `direct`) and
  `promote_from: [<environment names>]` (empty = any environment of the
  project): the protection policy, see below.
- `priority: normal | high` (default `normal`): the resource priority, see
  below. Raising to `high` or creating with `high` is instance admin only;
  lowering is project admin.

Creation: project admins, instance admins, and deployers (members with
`deploy` on at least one environment of the project) create environments;
the creator gets an explicit `deploy` cell on it, so a member locked out of
production still creates feature environments and always can use what they
create. Implicit creation on first deploy (`skali deploy --environment
feat-x`) is always `normal` priority; `high` needs an explicit create or a
later raise by an instance admin.

Creation defaults keyed by priority: `normal` environments start with
`member_access: deploy` (open, like a shared scratch environment); `high`
environments start with `member_access: read` and the CLI and console
suggest `promote-only` protection. Creation-time defaults only; the settings
are independent afterwards.

Deletion and teardown: `deploy` on the environment when it is `normal`
priority, project admin when it is `high`. Both stay behind sudo mode and
confirmation.

## Protection policy

`deploy_policy: promote-only` means the running revision of the environment
only changes through:

- promote from an environment in `promote_from` (or any environment of the
  project when the list is empty); the promoter needs `read` on the source
  and `deploy` on the target;
- rollback to a revision that was previously active in this environment;
- an explicit bypass (below).

A direct deploy is refused with `403 environment_protected` and a message
that names the promote command. The check runs in plan as well as in open,
so the CLI refuses before anything is built. Values changes, restore, exec,
and backups are not policy-gated: the policy is about untested code
reaching the environment, and those are configuration, data, and access.

The policy applies to everyone including project and instance admins.
Bypass: request field `bypass_protection: true` (`skali deploy
--bypass-protection`; `--force` already means "restart even when nothing
changed" and keeps that meaning). Allowed for project admins and instance
admins, requires a fresh session (sudo mode, `403 reauth_required`
otherwise, the CLI reauths and retries), recorded on the run and the
deployment, shown in the ready summary and the console.

## Priority

`priority: high` marks environments that must keep running when resources
are tight; `normal` environments yield. Only instance admins create or
raise environments to `high` because it is a cluster-wide resource decision,
not a project decision; project admins may lower.

What priority drives, in the permission slice: two PriorityClasses in the
system bundle (`skali-high` preempts `skali-normal`), rendered as
`priorityClassName` on application pods. The scheduler evicts normal-priority
pods when a high-priority pod cannot be placed, and the kubelet evicts
normal-priority pods first under node pressure. Later, under the roadmap's
resource item and referencing this field: request/limit defaults per
priority, an optional cluster-wide cap on normal-priority consumption via a
priority-scoped ResourceQuota, database placement (high on the production
pool, normal on the shared pool), and a graceful scale-down controller if
preemption proves too blunt.

Priority also selects the creation defaults and the delete/teardown rule
above. It does not imply protection; that stays an explicit setting.

## Who may do what

Legend: IA instance admin, PA project admin, D deployer (member with
`deploy` on at least one environment), M member with the named level on the
environment in question, S sudo mode required.

| action | who |
|---|---|
| create project | IA, or member with `create_projects` (creator becomes PA) |
| list projects | memberships only (IA: all) |
| read project, definitions, draft, members list | any member, IA |
| update project (display name), delete project (S) | PA, IA |
| submit definition, write draft | D, PA, IA |
| manage members and access cells (S) | PA, IA |
| environment settings: member default, protection (S) | PA, IA |
| priority raise to high or create high (S) | IA |
| priority lower (S) | PA, IA |
| create environment (normal) | D, PA, IA |
| list environments | filtered by level > none |
| environment status, deployments, revisions, runs, run logs, runtime logs, values (names), connection info, backups list, settings | M read |
| plan, deploy, promote into, rollback, values set/prune, backup create, restore into (S), run cancel | M deploy |
| exec (S), resolved application environment (S), credential reveal (S) | M deploy |
| delete/teardown normal-priority environment (S) | M deploy, PA, IA |
| delete/teardown high-priority environment (S) | PA, IA |
| bypass protection (S) | PA, IA |
| registry push `skali/<project>/<app>` | D, PA, IA |
| registry pull `skali/<project>/<app>` | any member of the project, IA |
| registry push/pull `cache/...` | D anywhere, IA |
| users, nodes, system observation, backup target (S for writes) | IA |
| own account, sessions, 2FA, `/system/meta`, `/auth/session` | any authenticated user |

## Route classification

Every route carries exactly one classification and a router-walk test fails
on any route without one, so a route added later cannot be open by
accident. Non-member or `none` answers 404 `not_found`; an insufficient
level answers 403 `forbidden` with a message naming the required level
("deploy access on environment production required").

Public: `GET /healthz`, `GET /openapi.yaml`, `GET /token` (registry realm,
Basic auth per request as today).

Authenticated, self: `POST /auth/logout`, `POST /auth/reauth`,
`GET /auth/session`, `GET /auth/sessions`, `DELETE /auth/sessions/{id}`,
`POST /auth/2fa/confirm`, `GET /system/meta`; sudo: `POST /auth/password`,
`POST /auth/2fa/enable`, `POST /auth/2fa/disable`,
`POST /auth/2fa/backup-codes`.

Instance admin: `GET /nodes`, `GET /system/observation` (both member-readable
today, the console only hides them), `GET /users`; sudo: `POST /users`,
`PATCH /users/{id}` (gains `create_projects`), `DELETE /users/{id}`,
`POST /users/{id}/password`, `GET|PUT|DELETE /system/backup-target`.

Project scoped:

| route | level |
|---|---|
| `POST /projects` | IA or `create_projects` |
| `GET /projects` | filtered |
| `GET /projects/{id}` | project read |
| `PATCH /projects/{id}` | project admin |
| `DELETE /projects/{id}` (S) | project admin |
| `GET /projects/{id}/draft` | project read |
| `PUT /projects/{id}/draft`, `POST /projects/{id}/definitions` | deployer |
| `POST /projects/{id}/environments` | deployer or admin; `high` IA |
| `GET /projects/{id}/environments` | filtered by level |
| `GET /projects/{id}/backups` | filtered by level |
| `GET /projects/{id}/members` (new) | project read |
| `PUT|DELETE /projects/{id}/members/{user}` (new, S) | project admin |
| `POST /artifacts/{id}/verify`, `POST /builds/{id}/heartbeat` | deployer of the owning project, plus today's actor rule |

Environment scoped (resource ids resolve to their environment first: runs,
steps, deployments, revisions):

| route | level |
|---|---|
| `GET /environments/{id}` | read |
| `PATCH /environments/{id}` (new: settings, S) | project admin; priority high IA |
| `PUT|DELETE /environments/{id}/access/{user}` (new, S) | project admin |
| `DELETE /environments/{id}`, `POST .../teardown` (S) | deploy if normal, project admin if high |
| `GET .../values` | read |
| `PUT .../values`, `DELETE .../values/{name}` | deploy |
| `GET .../revisions`, `GET /revisions/{id}`, `GET .../target` | read |
| `PUT .../target` (rollback) | deploy (allowed under promote-only) |
| `POST .../plan`, `POST .../deployments` | deploy, plus policy and bypass rules |
| `GET /deployments/{id}` | read |
| `POST /deployments/{id}/complete`, `.../fail` | deploy, plus actor rule |
| `POST /runs/{id}/cancel` | deploy |
| `POST /runs/{id}/steps`, `PATCH /steps/{id}`, `POST /steps/{id}/logs` | deploy, plus today's actor rule and artifacts-subtree rule |
| `GET .../status`, `.../status/stream` | read |
| `GET .../runs`, `.../runs/stream`, `GET /runs/{id}`, `.../stream`, `GET /steps/{id}/logs`, `.../stream` | read |
| `GET .../logs/stream` (runtime logs) | read |
| `GET .../exec` (S) | deploy |
| `GET .../databases/{key}/connection`, `.../buckets/{key}/connection` | read |
| `POST .../credentials/reveal` (S) | deploy |
| `GET .../applications/{key}/environment` (S) | deploy |
| `POST .../backups` | deploy |
| `GET .../backups` | read |
| `POST .../restore` (S) | deploy on target, read on the snapshot's environment |

## Registry token scope

`userActions` becomes access-aware: `skali/<project>/<app>` grants push and
pull to deployers, pull only to other members, nothing to non-members
(404-equivalent: the repository does not exist for them). `cache/...`
grants push and pull to anyone who is a deployer somewhere, nothing
otherwise. The `skali-node` pull credential is unchanged.

## What the API tells clients

So the CLI and console can hint and hide without extra calls, and so
`skali` can explain refusals before doing work:

- `GET /auth/session` gains `create_projects`.
- Project payloads gain `access: {role: admin|member, environments: {name:
  read|deploy}}` for the caller (instance admins report `admin`); the map
  lists only visible environments.
- Environment payloads gain `access: read|deploy` and `settings:
  {member_access, deploy_policy, promote_from, priority}`.
- Plan responses report the policy verdict (`protected`, allowed sources,
  whether the caller may bypass) so `skali deploy` refuses before building.
- Runs and deployments carry `bypass_protection` and the actor.

## Management surfaces

API first; then console and CLI, both thin over the same routes.

Console: users page gets the `create_projects` toggle; a project gets a
Members tab showing the members x environments matrix (default access, cells,
effective level) with editing for project admins; environment settings
(member default, protection, priority for instance admins); badges for
protected and high-priority environments; deploy/rollback/exec controls
follow `access`; nodes/system/users only for instance admins server-side,
not just hidden.

CLI (names open): `skali project members ls|add|set|rm`,
`skali env ls|create|set|rm|access` (`set` for member default, protection,
priority), `skali deploy --bypass-protection`, `skali remote status` shows
role and `create_projects`. Refusals print the reason and, for protection,
the promote command. Interactive first-deploy environment creation stays,
always `normal` priority.

`skali dev` is unaffected: the local skalid has one dev user who is an
instance admin.

## Journal and audit

Runs and deployments record the actor (as today) and `bypass_protection`.
Membership, cell, and environment-settings changes are logged as structured
events (slog) with actor, target, before, after; a durable audit table is
deferred until something needs to read it.

## Schema

- `project_members(project_id, user_id, role, default_access, created_at,
  updated_at)`, primary key `(project_id, user_id)`, cascades from both.
- `environment_access(environment_id, user_id, access)`, primary key
  `(environment_id, user_id)`, cascades from both.
- `environments`: `member_access` (default `deploy`), `deploy_policy`
  (default `direct`), `promote_from` (text array, default empty),
  `priority` (default `normal`), all `CHECK`-constrained.
- `users.create_projects` boolean default false.
- Bootstrap unchanged: `skalid user create --role admin`.

Backfill of existing installations: see open decisions.

## Enforcement shape

One resolver in a new `internal/authz` package: `Resolve(ctx, user,
projectID)` reads membership, cells, and environment settings in one query
and returns the role, the effective level per environment, and the derived
predicates (deployer, may create environments, may bypass). Instance admin
short-circuits. Handlers ask the resolver; middleware attaches only the
user, as today. List endpoints filter with the resolver's visible set.
`RequireAdmin` and `RequireFresh` stay for instance routes and sudo. The
policy gate lives beside the existing in-flight and destructive gates in
deploy open and in plan preview. Registry `userActions` calls the same
resolver.

## Testing

Resolver unit tests over the precedence table; router-walk test asserting
every route is classified; handler tests for 404 versus 403 and for
filtered lists; registry scope tests; the dev e2e gains a member scenario
(create a member, grant staging deploy, production 404, promote-only refusal
and bypass, environment creation by a deployer); console checks for hidden
versus enforced.

## Deferred on purpose

API and CI tokens (memberships are keyed by user id, a token becomes a
subject later without schema changes to the access model); teams or groups;
a durable audit table; approvals or required reviewers; the priority
mechanism beyond PriorityClasses (request defaults, quotas, database
placement, graceful scale-down); per-service permissions; OIDC group
mapping.

## Worked examples

- Sysadmin: instance admin. Sees and does everything, still cannot deploy
  directly into a promote-only production without `--bypass-protection` and
  a fresh session, and the run says so.
- Programmer on a customer site: member of that project, default `deploy`;
  production has `member_access: read` and `promote-only` from staging. They
  deploy staging, create feature environments, read production, and cannot
  put untested code there. Give them a `deploy` cell on production and they
  promote; they still cannot deploy directly.
- Log watcher: member, default `none`, cell `read` on staging. Production
  does not exist for them.
- Internal-tools colleague: `create_projects` on, admin of their own
  projects, member nowhere else. Their environments are `normal` priority
  and yield to production; they cannot raise them.

## Open decisions

Written into the model above as proposals; each can still be flipped.

1. Runtime logs at `read` (as written) or at `deploy` because application
   logs are raw pass-through and may print secrets. Run logs are redacted
   either way.
2. Default access for a newly added member: `read` (as written) or `deploy`.
3. Bypass flag name: `--bypass-protection` (as written) or something
   shorter.
4. Backfill on upgrade: no memberships (least privilege; existing `member`
   users lose product access until granted; nothing changes for admins) or
   preserve today's behavior by granting every existing member `deploy` on
   every existing project. Lean: no memberships; the khz installation has
   only the admin today.
5. Sudo for rollback and restore into a protected environment, in addition
   to what is sudo today. Lean: yes for both, they are cheap and rare.
6. Should protection also refuse teardown and delete of the protected
   environment, or is priority `high` (admin-only teardown) enough. Lean:
   priority is enough; keep protection about revisions.
7. Order of the management surfaces after the API: console first (as
   written) or CLI first.
8. CLI naming: `skali project members ...` and `skali env ...`, or fold
   both under one `skali access` group.
