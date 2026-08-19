# Permissions

Status: design agreed 2026-08-19. The server side (schema, resolver, every
route classified and enforced, registry scope, members/cells/settings API,
payload additions) is implemented; the console and CLI management surfaces,
the promote-only policy enforcement with its bypass, and the PriorityClass
rendering are pending. This file is the description of how access works;
the "Open decisions" section at the end disappears as they are settled.

## Why

People with access to one skali installation have very different trust
levels. The motivating team:

- The sysadmin sets up skali and the hosts and may do everything.
- A programmer works on customer projects but must not reach every project,
  and on the projects they work on they maintain staging while production
  is read-only or promote-only for them.
- A colleague without infra knowledge hosts small internal tools. They own
  their own projects and are a member nowhere else.

Principles:

- One ladder of roles, used at the environment and at the project level
  with the same words. No permission matrices per action.
- The environment is the smallest unit of control. Everything with a blast
  radius or a secret (deployments, runs, logs, values, exec, credentials,
  backups, routes) is environment scoped. The project is the container and
  the place where the defaults come from.
- Access (who may act) and policy (which kind of act is allowed for anyone)
  are separate axes. Protection applies to admins too; admins bypass it only
  explicitly and it is recorded.
- Deny by default, enforced server-side in one place. The console and CLI
  hide, disable, and explain; they never enforce.
- Roles are code, not data: the database stores which role a user has,
  never what a role means.
- Nothing lives in `skali.yml`. The manifest is environment-agnostic and
  whoever can deploy could edit it, so roles, protection, and priority are
  server-side settings.

## The ladder

`none < read < deploy < maintain < admin`. Each step includes everything
below it. The question that places an action on a step: what is the
smallest thing this person needs to change?

| role | on an environment |
|---|---|
| `none` | locked. The environment still appears in the project's environment list (id, name, the fact that it is locked) so names and their collisions are visible, but everything inside it answers 403: no status, runs, deployments, values, settings. |
| `read` | everything visible: status and health, deployments, revisions, runs and run logs (redacted), runtime logs, value names and versions, routes, database and bucket connection info without secrets, backups list, environment settings, who has access to it. |
| `deploy` | change only what code runs: promote into this environment, rollback, restart, run cancel, backup create, and direct deploys whose definition is unchanged (the manifest compiles to the environment's currently active definition version, so only image digests differ). |
| `maintain` | the full blast radius of `skali.yml`: direct deploys that change the definition (services, routes, databases, buckets, including `--allow-destructive`), set and prune values, restore, and the secret-bearing reads: exec, resolved application environment, credential reveal. |
| `admin` | management outside the yaml: this environment's protection policy and role ceiling, other users' roles on this environment, lowering priority, delete and teardown, bypassing protection. |

Notes on the placement:

- `deploy` is a change-control boundary, not a secret boundary. A `deploy`
  user ships code that runs with the environment's values, so values and
  the secret-bearing reads live together one step up in `maintain`:
  whoever may change the configuration may also read it. What `deploy`
  gives is "devs ship code, maintainers own topology and config" and
  "anyone may release the tested staging revision to production".
- "Definition unchanged" is an equality check: definitions are
  content-addressed, so the server compares the submitted definition
  version with the environment's active one. No diffing. Consequences: the
  first deploy into an empty environment needs `maintain` (nothing to be
  unchanged against), `--env-file` and `--prune-values` are refused for
  `deploy`, and a promotion may carry a definition change because a
  maintainer already made that revision real in the source environment.
- Raising priority to `high` is not on the ladder; it is an instance-admin
  act (see Priority).

## Instance layer

`users.role` stays `admin | member`.

- `admin`: everything. Effective `admin` on every project and environment.
  Manages users, nodes, system, backup targets. Only instance admins create
  or raise environments to `high` priority.
- `member`: nothing until granted through project membership.

One extra per-user permission for members: `create_projects` (default off,
set by instance admins). Creating a project makes the creator that
project's `admin`. Instance admins always may create projects and also get
an explicit `admin` membership row on projects they create, so a later
demotion to member keeps their own projects.

Invariants kept from today: at least one instance admin must remain
(`ErrLastAdmin`), users cannot change their own instance role or delete
themselves, `skalid user set-role` is the lost-admin recovery path
(`skalid user delete` must go through `auth.DeleteUser` so it honors the
last-admin guard; today it bypasses it).

## Project level

Membership: `(project, user) -> role` on the same ladder (`read | deploy |
maintain | admin`; `none` is not a project role, a non-member simply has no
row). Non-members do not see the project: lists are filtered, direct access
answers 404, exactly like a project that does not exist. A colliding name
at project creation answers 409 as today.

The project role is the user's default role on every environment of the
project, and each step adds the project-level rights that are not an
environment:

| project role | default environment role | adds at project level |
|---|---|---|
| `read` | `read` | the project is visible; list environments (locked ones included); read definitions and the draft; members list; project-wide backups list filtered to environments with at least `read`; registry pull |
| `deploy` | `deploy` | submit definitions and write the draft, registry push: what a code-only deploy needs |
| `maintain` | `maintain` | create environments; the creator gets an explicit `admin` cell on the environment they created (members below project `admin` only; admins are admin by rule anyway), so they manage and delete their own test environments without a project admin |
| `admin` | `admin` | members and cells, project settings, delete project |

Deployer: a project role of `deploy` or higher, or any cell of `deploy` or
higher, however obtained. Deployers may submit definitions, write the draft,
push to the project's registry repositories, and drive artifact
verification and build heartbeats. This makes a `read` member with a
`deploy` cell on staging able to actually deploy there. The project role
counts even when every environment is capped below `deploy` or none exists
yet, so the first definition can land before the first environment;
definition versions are content-addressed and pushing an image changes
nothing that runs.

There is no "last project admin" invariant: instance admins are implicit
admins everywhere, so a project may end up with zero explicit admins.

## Cells, ceiling, effective role

Picture a project's access as a grid: one row per member, one column per
environment, each box the role that person has on that environment. Most
boxes are not stored; they are the member's project role repeated across
the columns. Two kinds of exceptions exist:

- A **cell** is one box filled in by hand: `(environment, user) -> role`,
  any step of the ladder including `none`. It overrides the project role
  for that one person on that one environment, up or down (`read` member
  with `maintain` on staging; `maintain` member with `none` on production).
  A cell needs a membership to hang off: setting one for a non-member
  answers 409, and removing the member drops their cells.
- The **ceiling** is one environment setting, `max_role`, that caps the role
  members inherit from their project role on that environment. Default
  `admin` (no cap). "Production is read-only for everyone unless named" is
  `max_role: read` once, instead of one cell per member. Cells are not
  capped; they are the way to name exceptions above the ceiling.

Effective role of a user on an environment, first match wins:

    1. instance admin                       -> admin
    2. a cell exists for (user, environment) -> the cell
    3. project role is admin                 -> admin   (project admins are never capped)
    4. otherwise                             -> min(project role, environment max_role)

Example:

                    staging     production   feat-x
    alice (admin)   admin       admin        admin
    bob (maintain)  maintain    read*        maintain
    carol (read)    read        read         deploy*

Starred boxes are cells. Bob on production is rule 2 (`read`), on staging
rule 4 (`maintain`, no cap). Carol on feat-x is rule 2, on staging rule 4.
If production's ceiling were `read` and bob had no cell there, rule 4 would
give him `read` as well. Every check the server makes ("is this at least
`deploy`?") is made against the effective role.

## Environment settings

Server-side, edited by environment admins unless noted:

- `max_role: none | read | deploy | maintain | admin` (default `admin`, see
  creation defaults): the ceiling above.
- `deploy_policy: direct | promote-only` (default `direct`) and
  `promote_from: [<environment names>]` (empty = any environment of the
  project): the protection policy.
- `priority: normal | high` (default `normal`). Creating with `high` or
  raising to `high` is instance admin only; lowering is environment admin.

Creation: project `maintain` and up create environments; the creator gets an
explicit `admin` cell. Implicit creation on first deploy (`skali deploy
--environment feat-x`) follows the same rule and is always `normal`
priority. Creation defaults by priority: `normal` starts with `max_role:
admin` (open, a shared scratch environment); `high` starts with `max_role:
read` and the CLI and console suggest `promote-only`. Creation-time
defaults only; the settings are independent afterwards.

Deletion and teardown: environment `admin`, behind sudo mode and
confirmation as today.

## Protection policy

`deploy_policy: promote-only` means the running revision of the
environment only changes through:

- promote from an environment in `promote_from` (or any environment of the
  project when the list is empty); the promoter needs `read` on the source
  and `deploy` on the target;
- rollback to a revision that was previously active in this environment;
- an explicit bypass (below).

A direct deploy is refused with `403 environment_protected` and a message
that names the promote command. The check runs in plan as well as in open,
so the CLI refuses before anything is built. Values changes, restore, exec,
and backups are not policy-gated: the policy is about untested code
reaching the environment; those are configuration, data, and access, and
have their own roles.

The policy applies to everyone including admins. Bypass: request field
`bypass_protection: true` (`skali deploy --bypass-protection`; `--force`
already means "restart even when nothing changed" and keeps that meaning).
Requires environment `admin` (project admins and instance admins included)
and a fresh session (sudo mode, `403 reauth_required` otherwise, the CLI
reauths and retries). Recorded on the run and the deployment, shown in the
ready summary and the console.

## Priority

`priority: high` marks environments that must keep running when resources
are tight; `normal` environments yield. Only instance admins create or
raise environments to `high` because it is a cluster-wide resource decision,
not a project decision.

In the permission slice, priority drives two PriorityClasses in the system
bundle (`skali-high` preempts `skali-normal`), rendered as
`priorityClassName` on application pods: the scheduler evicts
normal-priority pods when a high-priority pod cannot be placed, and the
kubelet evicts normal-priority pods first under node pressure. It also
selects the creation defaults above. Later, under the roadmap's resource
item and referencing this field: request and limit defaults per priority,
an optional cluster-wide cap on normal-priority consumption via a
priority-scoped ResourceQuota, database placement (high on the production
pool, normal on the shared pool), and a graceful scale-down controller if
preemption proves too blunt. Priority does not imply protection; that is
an explicit setting.

## Who may do what

Legend: IA instance admin; P:x project role x or higher; E:x effective
role x or higher on the environment in question; D deployer; S sudo mode.

| action | who |
|---|---|
| create project | IA, or member with `create_projects` (creator becomes P:admin) |
| list projects | memberships only (IA: all) |
| read project, definitions, draft, members list, environment list | P:read |
| update project settings, delete project (S) | P:admin |
| manage members and cells (S) | P:admin (cells of one environment also E:admin) |
| submit definition, write draft, registry push, artifact verify, build heartbeat | D |
| registry pull | P:read |
| create environment (`normal`) | P:maintain |
| create or raise environment to `high` (S) | IA |
| environment settings: ceiling, protection, lower priority (S) | E:admin |
| delete, teardown (S) | E:admin |
| status, deployments, revisions, runs, run logs, runtime logs, value names, connection info, backups list, settings, access list | E:read |
| promote into, rollback, restart, run cancel, backup create, direct deploy with unchanged definition | E:deploy |
| direct deploy with definition changes, values set/prune, restore (S) | E:maintain |
| exec (S), resolved application environment (S), credential reveal (S) | E:maintain |
| bypass protection (S) | E:admin |
| users, nodes, system observation, backup target (S for writes) | IA |
| own account, sessions, 2FA, `/system/meta`, `/auth/session` | any authenticated user |

## Route classification

Every route carries exactly one classification and a router-walk test fails
on any route without one, so a route added later cannot be open by
accident. Error semantics: non-member of the project answers 404
`not_found`; a locked environment or an insufficient role answers 403
`forbidden` with a message naming the required role ("maintain on
environment production required: this deploy changes the definition");
protection answers 403 `environment_protected`; a bypass without a fresh
session answers 403 `reauth_required`.

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

| route | requirement |
|---|---|
| `POST /projects` | IA or `create_projects` |
| `GET /projects` | filtered to memberships |
| `GET /projects/{id}` | P:read |
| `PATCH /projects/{id}` | P:admin |
| `DELETE /projects/{id}` (S) | P:admin |
| `GET /projects/{id}/draft` | P:read |
| `PUT /projects/{id}/draft`, `POST /projects/{id}/definitions` | D |
| `POST /projects/{id}/environments` | P:maintain; `priority: high` IA |
| `GET /projects/{id}/environments` | P:read; locked environments carry only id, name, `access: none` |
| `GET /projects/{id}/backups` | P:read; filtered to environments with E:read; snapshots of environments the control plane no longer has are shown to P:admin only |
| `GET /projects/{id}/members` (new) | P:read |
| `PUT|DELETE /projects/{id}/members/{user}` (new, S) | P:admin |
| `POST /artifacts/{id}/verify`, `POST /builds/{id}/heartbeat` | D of the owning project (an artifact or build without one is instance-admin only) |

Environment scoped (runs, steps, deployments, and revisions resolve to
their environment first):

| route | requirement |
|---|---|
| `GET /environments/{id}` | P:read; locked answers the minimal shape (id, name, project, `access: none`) |
| `PATCH /environments/{id}` (new: settings, S) | E:admin; `priority: high` IA |
| `GET /environments/{id}/access` (new) | E:read |
| `PUT|DELETE /environments/{id}/access/{user}` (new, S) | E:admin |
| `DELETE /environments/{id}`, `POST .../teardown` (S) | E:admin |
| `GET .../values` | E:read |
| `PUT .../values`, `DELETE .../values/{name}` | E:maintain |
| `GET .../revisions`, `GET /revisions/{id}`, `GET .../target` | E:read |
| `PUT .../target` (rollback) | E:deploy (allowed under promote-only) |
| `POST .../plan`, `POST .../deployments` | E:deploy when the definition is unchanged or for a promotion, E:maintain when it changes, is the first, stages values (`candidate_id`), or prunes values; plus policy and bypass rules; promote needs E:read on the source (a source outside the project answers 404); refused before any preview work |
| `GET /deployments/{id}` | E:read |
| `POST /deployments/{id}/complete`, `.../fail` | E:deploy |
| `POST /runs/{id}/cancel` | E:deploy |
| `POST /runs/{id}/steps`, `PATCH /steps/{id}`, `POST /steps/{id}/logs` | E:deploy, plus today's actor rule and artifacts-subtree rule |
| `GET .../status`, `.../status/stream` | E:read |
| `GET .../runs`, `.../runs/stream`, `GET /runs/{id}`, `.../stream`, `GET /steps/{id}/logs`, `.../stream` | E:read (a run outside any environment is instance-admin only) |
| `GET .../logs/stream` (runtime logs) | E:read |
| `GET .../exec` (S) | E:maintain |
| `GET .../databases/{key}/connection`, `.../buckets/{key}/connection` | E:read |
| `POST .../credentials/reveal` (S) | E:maintain |
| `GET .../applications/{key}/environment` (S) | E:maintain |
| `POST .../backups` | E:deploy |
| `GET .../backups` | E:read |
| `POST .../restore` (S) | E:maintain on the target, E:read on the snapshot's environment (a source the control plane no longer has is nobody's to protect) |

## Registry token scope

`userActions` becomes access-aware: `skali/<project>/<app>` grants push and
pull to deployers of the project, pull only to other members, nothing to
non-members (the repository does not exist for them). `cache/...` grants
push and pull to anyone who is a deployer somewhere, nothing otherwise. The
`skali-node` pull credential is unchanged.

## What the API tells clients

So the CLI and console can hint, disable, and explain without extra calls,
and so `skali` can refuse before doing work:

- `GET /auth/session` gains `create_projects`.
- Project payloads gain `access: {role, environments: {name: role}}` for
  the caller, with effective roles (instance admins report `admin`
  everywhere); locked environments appear with `none`.
- Environment payloads gain `access: <effective role>` and `settings:
  {max_role, deploy_policy, promote_from, priority}`.
- Plan responses report `required_role` (`deploy` or `maintain`, from
  whether the definition changed) and the policy verdict (`protected`,
  allowed sources, whether the caller may bypass), so `skali deploy`
  refuses before building and the console disables the right buttons.
- Runs and deployments carry the actor and `bypass_protection`.

## Management surfaces

API first; then console and CLI, both thin over the same routes.

Console: users page gets the `create_projects` toggle; a project gets a
Members tab showing the grid (members x environments, project role, cells,
effective role) with editing for project admins; environment settings
(ceiling, protection, priority for instance admins); the environment
dropdown lists locked environments disabled with a lock icon; badges for
protected and high-priority environments; deploy, rollback, values, exec
controls follow `access`; nodes, system, and users only for instance admins
server-side, not just hidden.

CLI (names open): `skali project members ls|add|set|rm`, `skali env
ls|create|set|rm|access` (`set` for ceiling, protection, priority; `access`
for cells), `skali deploy --bypass-protection`, `skali remote status` shows
role and `create_projects`. Refusals print the required role and, for
protection, the promote command. Interactive first-deploy environment
creation stays, always `normal` priority.

`skali dev` is unaffected: the local skalid has one dev user who is an
instance admin. `skali remote status` prints the instance role and whether
the user may create projects.

## Journal and audit

Runs and deployments record the actor (as today) and `bypass_protection`.
Membership, cell, and environment-settings changes are logged as structured
events (slog) with actor, target, before, after; a durable audit table is
deferred until something needs to read it.

## Schema

- `project_members(project_id, user_id, role, created_at, updated_at)`,
  primary key `(project_id, user_id)`, cascades from both; `role` checked
  against `read | deploy | maintain | admin`.
- `environment_access(environment_id, project_id, user_id, role,
  created_at, updated_at)`, primary key `(environment_id, user_id)`;
  composite foreign keys to `environments(id, project_id)` and to
  `project_members(project_id, user_id)`, both cascading, so a cell can
  only reference an environment of the project whose membership it
  extends and leaves with the member; `role` checked against `none | read
  | deploy | maintain | admin`.
- `environments`: `max_role` (default `admin`), `deploy_policy` (default
  `direct`), `promote_from` (text array, default empty), `priority`
  (default `normal`), all `CHECK`-constrained.
- `users.create_projects` boolean default false.
- Bootstrap unchanged: `skalid user create --role admin`.

Backfill of existing installations: none. Existing `member` users keep
their accounts and hold no project membership until an admin grants one;
`skalid migrate up` prints a notice naming them when migration 00019
lands (on a cluster that is the migrate init container's log). Existing
admins are unaffected.

## Enforcement shape

One resolver in `internal/authz`: `Project(ctx, user, projectID)` reads the
membership, the project's environments with their settings, and the user's
cells (three reads however many projects are asked for; `All` does the
same for every visible project) and returns a grant: the project role, the
effective role per environment, and the derived predicates (deployer, may
create environments). Instance admin short-circuits. The meaning of a role
is code (`authz.Effective`), never data.

Every `/v1` route is registered in `internal/api/router.go` through
`access.route` with exactly one route class (`project:read`,
`environment:deploy`, `run:read`, `artifact:deployer`, ...). The class
installs the scope middleware that parses the id, loads the addressed
entity (environment-owned entities resolve to their environment first),
resolves the grant, and answers 404 (unknown or not visible) or 403 (role
too low, message naming the role) before the handler runs; on success the
grant and the loaded row ride on the request context. Handlers refine
where one route needs more than a minimum role: plan and open compare the
submitted definition version with the active revision's, environment
creation checks `priority: high` against the instance role, project
creation checks `create_projects`. The router-walk test asserts every
registered route carries a class, and the access matrix test probes every
route with one fixture per rung. `RequireAdmin` and `RequireFresh` stay
for instance routes and sudo; the access middleware sits inside the sudo
groups, so a stale session answers `reauth_required` before 404 or 403
(nothing leaks: the caller still has to prove identity to learn more).
The registry token realm asks the same resolver.

## Testing

Resolver unit tests over the effective-role rules; router-walk test
asserting every route is classified; an access matrix test probing every
route with one fixture per rung (non-member 404, locked 403, one step
below 403 naming the role, allowed not refused); handler tests for locked
listings, filtered lists, members and cells, creation defaults, the
deploy/maintain boundary on plan and open; registry scope tests; the dev
e2e gains a member scenario (create a member, project `read` plus a `deploy`
cell on staging, code-only deploy allowed, definition change refused with
the required role, production locked by ceiling, promote-only refusal and
bypass, environment creation by `maintain`); console checks for disabled
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
- Programmer on a customer site: project `maintain`; production has
  `max_role: read` and `promote-only` from staging. They maintain staging,
  create feature environments (admin on those), read production, and cannot
  put untested code there. Give them a `deploy` cell on production and they
  promote; they still cannot deploy directly or touch production values.
- Release manager: project `read` plus a `deploy` cell on production. They
  promote the tested staging revision to production and roll back, and
  change nothing else.
- Log watcher: project `read` plus a `none` cell on production. Production
  shows up locked in their list; its contents answer 403.
- Internal-tools colleague: `create_projects` on, `admin` of their own
  projects, member nowhere else. Their environments are `normal` priority
  and yield to production; they cannot raise them.

## Open decisions

Settled 2026-08-19: runtime logs at `read`, backup create at `deploy`,
environment creation at project `maintain`, no membership backfill on
upgrade (existing `member` users hold no product access until granted;
`skalid migrate` prints a notice naming them).

Still open, each can be settled in the slice it belongs to; the text above
reflects the lean:

1. Bypass flag name: `--bypass-protection` (as written) or something
   shorter. (Protection slice.)
2. Sudo for rollback and restore into a protected environment, in addition
   to what is sudo today. Lean: yes for both. (Protection slice.)
3. Should protection also refuse teardown and delete of the protected
   environment (both are environment `admin` already). Lean: no; keep
   protection about revisions. (Protection slice.)
4. Order of the management surfaces after the API: console first (as
   written) or CLI first. (Management slice.)
5. CLI naming: `skali project members ...` and `skali env ...`, or one
   `skali access` group. (Management slice.)
