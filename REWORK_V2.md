# Skali v2 rework plan

Status: architecture proposal and implementation plan

Date: 2026-07-19

Amended 2026-07-27: mission and host-ownership doctrine (section 2),
existing-cluster mode removed (sections 14.1, 16, 18, 19), R4 landing notes
recorded (section 16).

Recommended starting point: `c4eb838` (post-demolition, before the current Kubernetes rework)

## 1. Executive decision

Skali v2 should rebuild the product and reconciliation core from the clean
post-demolition commit, while preserving the independent foundation that was
already sound: authentication, users, session security, the API/store plumbing,
CLI contexts, the SvelteKit shell, and reusable UI components.

The current `rework` branch remains a reference implementation and behavioral
test catalog. Feature commits from it should not be merged wholesale. Useful
behavior, tests, and UI components should be ported deliberately after the new
model exists.

Development is headless-first. The definition engine, public API, CLI,
reconciliation kernel, observed-state projections, and execution journal must
form a complete usable product before the web UI is rebuilt. The CLI is the
first rich client. The later web UI is a visual definition editor and status/run
viewer over exactly the same contracts, not a second way to manage services.

The v2 foundation has four connected parts:

1. A portable project definition selected explicitly or discovered as
   `skali.yml`/`skali.yaml`.
2. An immutable, resolved revision made from that definition and an
   environment's values.
3. A continuously maintained observed-state store representing the cluster and
   external systems.
4. A persistent execution journal showing deployments as runs containing
   steps, attempts, progress, and detailed logs.

Only the first three are authoritative state. The execution journal explains
what the controller is doing; it must never be required to recover or decide
what should exist.

Between definition and revision sits a first-class artifact pipeline. An
application may reference an existing image or a build directory. A build may
run on the developer's machine or on a Skali-managed builder, but every path
produces the same content-addressed artifact in the installation's managed OCI
registry. External images are imported into that registry as a durable cache
before a revision targets them.

Local development is a separate local Skali installation managed by the CLI,
not a secondary cluster registered with a production Skali instance. It uses
the same `skalid`, schema, revision builder, service modules, Kubernetes
drivers, observation system, and reconciliation engine as a production
installation.

The product has two deliberately separate binaries carrying three operational
roles. `skali` is the developer CLI and rich API client, and it also carries
the privileged, recoverable installation and cluster-maintenance role as the
`skali cluster` command group. `skalid` is the continuously running product
control plane inside Kubernetes. The `skali cluster` group, not `skalid` or
the developer-workflow commands, owns host-level K3s creation, node joining,
Kubernetes upgrades, diagnosis, repair, and removal.

## 2. Product vision

### Mission (decided 2026-07-27)

Skali is an open-source, self-hostable alternative to hosted application
platforms such as Vercel, Render, and Railway, and to self-hosted deployment
tools such as Coolify. Its user brings machines, not a Kubernetes cluster:
Skali turns those machines into a platform and deploys applications onto it.
Kubernetes is the execution substrate because it already provides
battle-tested multi-server container orchestration, but it is an
implementation detail of the product, never its user-facing interface. The
measure of the product is that an operator goes from fresh hosts to a
deployed application without running kubectl or learning Kubernetes
concepts.

Two consequences are doctrine:

- Skali is deliberately both a cluster manager and an application platform.
  Host provisioning, node lifecycle, and Kubernetes upgrades are product
  surface, not clutter around the product.
- Skali owns its hosts. The only supported substrate is the exact
  combination Skali provisions and upgrades: one pinned k3s version per
  release plus the pinned operator bundle, on native Linux hosts or inside
  the Lima VM Skali manages on macOS. One tested combination replaces a
  support matrix, and no feature is designed around hosts Skali does not
  control.

### Product shape

Skali is an opinionated product control plane over Kubernetes. A developer
declares a project in product-level terms such as applications, databases,
object storage, connections, routes, and runtime requirements. Skali resolves
that declaration for an environment, continuously reconciles it onto a cluster,
maintains a fresh model of the resulting runtime, and explains every operation
in product language.

Kubernetes remains the execution substrate. Skali does not recreate scheduling,
container lifecycle management, networking, service discovery, or operator
behavior. Its product value is:

- A portable project model above raw Kubernetes objects.
- Reproducible, environment-aware deployments.
- Managed application, database, and object-storage systems.
- Immediate product-level knowledge of cluster state and health.
- Clear execution traces instead of opaque controller activity.
- The same workflow and runtime model on a laptop and a real cluster.
- A CLI that owns developer workflows rather than merely mirroring REST routes.
- A managed artifact path for local builds, cloud builds, and imported images.
- A headless engine that remains fully useful without the web application.

## 3. Current product baseline

This is the high-level behavior present on the current `rework` branch. It is an
inventory of product knowledge to preserve or consciously redesign, not a list
of implementation that should be copied.

### 3.1 Foundation and security

Implemented:

- Go control-plane daemon (`skalid`) with REST API and controller in one binary.
- SvelteKit BFF: browser session cookie remains server-side and bearer tokens do
  not reach browser JavaScript.
- Email/password login with argon2id.
- Opaque, revocable, sliding-expiry sessions.
- Optional TOTP two-factor authentication and backup codes.
- Reauthentication/sudo window for sensitive actions.
- Admin/member roles and operator-created users; no public signup.
- Login rate limiting, structured logging, OpenTelemetry hooks, configuration,
  migrations, sqlc, pgx, and isolated database tests.
- CLI authentication and kubectl-style named contexts.

V2 disposition: retain this foundation. Review and port the small security fixes
made after `c4eb838`, but do not redesign authentication as part of the core
rewrite.

### 3.2 Projects, environments, and configuration

Implemented:

- Projects as the top-level product grouping.
- Multiple independent environments per project.
- Environment duplication that copies configuration but not state.
- Environment-scoped service names and Kubernetes namespaces.
- High/low environment priority policy.
- Staged service creation, edits, and deletion.
- Application-to-database and application-to-bucket connections.

Current limitation: the editable project model is spread across normalized
rows, while an environment deployment stores a second JSON document. A future
manifest file was planned as a third consumer rather than being the original
source model.

V2 disposition: keep projects and environments as product concepts. Replace the
current draft representation with one typed project-definition document plus
separate typed environment values.

### 3.3 Deployments

Implemented:

- Environment-wide deployments rather than per-service releases.
- Diff/confirmation before deployment.
- Immutable deployment snapshots.
- Container image tag resolution and digest pinning.
- One in-flight rollout per environment.
- Pending, deploying, active, superseded, failed, and cancelled states.
- Failed or cancelled deployment fallback to the prior active deployment.
- Deployment history and basic timelines.
- Staged deletion that becomes destructive only when deployed.

Current limitation: deployment behavior, service-specific preparation,
database placement, state transitions, event narration, and rollback behavior
are interleaved. The timeline consists of broad events rather than a detailed
execution trace.

V2 disposition: preserve the product behavior but rebuild it around immutable
revisions, an explicit target pointer, and persistent runs/steps/attempts.

### 3.4 Application services

Implemented:

- Container-image applications.
- Fixed replicas and CPU-based autoscaling ranges.
- CPU, memory, and ephemeral-storage policy.
- Environment variables.
- One main listening port plus per-route target ports.
- HTTP health paths compiled into readiness/liveness probes.
- Domains and paths through Traefik.
- Automatic TLS through cert-manager.
- Persistent local-path volumes with the honest single-replica constraint.
- Start, stop, restart, staged removal, and restore-removal actions.
- Pod-level status, rollout diagnostics, live metrics, and sampled metrics
  history.
- Connections that inject database or S3 values and secret references.

Missing or incomplete:

- Application log aggregation/streaming is a stub.
- Git/local source builds are not implemented.
- Sidecars, workers, cron jobs, secret-typed variables, and multi-port process
  models are deferred.
- RWX storage and multi-replica stateful applications are deferred.

V2 disposition: application is one of the three initial service systems. Image
deployment remains supported, but a local build source must be part of the
manifest model early enough for `skali dev` to be a real workflow.

### 3.5 Database services

Implemented:

- PostgreSQL through CloudNativePG.
- Logical databases and roles on shared physical clusters.
- Shared, project, and dedicated isolation policies.
- Single, asynchronous, and synchronous availability policies for owned
  clusters.
- Engine/major-version catalog and extension validation.
- Generated database identities and Kubernetes-held credentials.
- Shared-pool defaults and admin resizing.
- CNPG status, primary/replica topology, and database connection injection.
- Pool placement and some pool lifecycle rules.
- A separate system database pool for SeaweedFS metadata.

Missing or incomplete:

- User-visible backup, restore, point-in-time recovery, and relocation flows.
- Capacity spill and safe rebalancing.
- External database access.
- Additional engines.

Current limitation: user databases and system databases use related concepts,
but system consumers are special controller paths. Pooling, physical database
clusters, logical tenants, credentials, placement, and product services are not
cleanly separated.

V2 disposition: create a lower-level database subsystem used equally by
project database services and internal platform claims such as the object-store
metadata database.

### 3.6 Object-storage services

Implemented:

- SeaweedFS as the blessed object-storage engine.
- Platform masters, filer, S3 gateway, and volume servers.
- A CNPG-backed filer metadata database.
- User bucket services with hard quotas.
- Generated S3 access keys stored in Kubernetes Secrets.
- Application-to-bucket connection injection.
- External S3 endpoint configuration and TLS ingress.
- Subsystem status, volume placement, bucket usage, and basic replication
  modeling.
- Node capability gates for object-storage roles.

Missing or incomplete:

- Erasure-coding workflows, evacuation, and richer bucket policies.
- Bucket history, public-read policy, lifecycle rules, and user-facing backup
  or replication controls.

Current limitation: SeaweedFS bootstrap, its system database, bucket
reconciliation, settings, and health were added through several special paths.

V2 disposition: object storage is the third initial service system. Separate
the physical object-store subsystem from logical bucket claims. Its metadata
database must be an ordinary internal consumer of the shared database
subsystem.

### 3.7 Cluster and platform management

Implemented or designed:

- k3s as the single Kubernetes distribution.
- Server-side apply and owned-object pruning.
- Some informer-driven wakeups plus periodic reconciliation.
- CloudNativePG, cert-manager, Traefik, SeaweedFS, metrics-server, and kube-vip
  as pinned platform components.
- Node capability labels for application, database, object-storage, master,
  and edge placement.
- Cluster/node/pod topology, capacity, metrics, subsystem health, database
  pools, and object-storage pages.
- External control plane: `skalid` and its Postgres state were designed to
  survive a broken tenant cluster.
- Single-server production topology with a future three-server HA design.

V2 disposition: retain Kubernetes as the substrate, but replace the external
control-plane topology. `skalid` runs inside the Kubernetes cluster by default;
a separate privileged installer owns K3s and installation lifecycle outside the
normal reconciler. Rebuild cluster observation before implementing product
services. Node capabilities and placement policies should be policies consumed
by service modules, not assumptions embedded throughout the controller.

### 3.8 Observability and clients

Implemented:

- Cached service statuses and severity rollups.
- Durable activity events.
- SSE doorbells that trigger UI refetches.
- Deployment timeline pages.
- Pod and service metrics plus limited history.
- Web pages for projects, environments, services, deployments, activity,
  system health, cluster topology, database pools, object storage, settings,
  users, and account security.
- CLI auth/context commands.

Missing or incomplete:

- Detailed deployment steps and logs.
- Application log streaming or retention.
- `skali deploy`, manifest workflows, local build workflows, and `skali dev`.
- An integrated managed registry and a shared local/cloud build artifact path.
- A packaged production installer.

V2 disposition: separate current state, state transitions, operation traces,
runtime logs, and UI invalidation into distinct systems. Treat the CLI as the
first complete client and add the visual UI after the headless deployment flow
works end to end.

## 4. Why a core rewrite is justified

The current implementation is functional and well tested, but its extension
cost is growing in the wrong direction:

- A new service kind must be threaded through API handlers, draft tables,
  environment duplication, definition serialization, diffing, deployment,
  controller switches, teardown, capability checks, status mapping, and UI
  unions.
- Mutable rows and immutable deployment definitions overlap without one
  canonical project schema.
- Some environment-specific values are described as outside deployment
  definitions while the current application snapshot includes them.
- Cluster state is partly queried on demand, partly watched, partly polled, and
  partly cached in database status columns.
- User services and platform subsystems use similar infrastructure through
  different lifecycle paths.
- Activity events narrate outcomes but cannot explain a deployment at the
  granularity users expect.
- The manifest and local runtime were postponed, even though both constrain
  the core definition and deployment architecture.
- Build execution, artifact upload, image caching, environment-file import, and
  deployment do not yet form one coherent workflow.

Continuing to add these as adapters around the present model would preserve the
behavior but not solve the underlying lack of a single extensible lifecycle.

## 5. Architectural principles

### 5.1 Three authoritative planes

1. **Definition plane:** portable user intent and typed environment values.
2. **Revision plane:** immutable, validated, resolved target state.
3. **Observation plane:** the freshest known state of Kubernetes and external
   systems, with explicit source and freshness.

No field may silently belong to more than one plane. Cached projections may be
stored for performance, but must identify their source and must never become
competing intent.

### 5.2 Reconciliation is level-triggered

The controller compares a target revision with current observation and performs
idempotent work. Nothing required for recovery lives only in a goroutine, queue,
or event stream. A restart performs an initial observation sync and continues
toward the same target.

### 5.3 Watches make the system responsive; resync makes it safe

Normal cluster knowledge comes from Kubernetes LIST/WATCH caches, not request-
time lookups or full polling passes. Watch changes update the observed-state
store and enqueue only affected owners. Periodic full resynchronization remains
the correctness backstop.

### 5.4 Product services own their complete lifecycle

Application, database, and object-storage modules own their schema,
validation, preparation, dependency declaration, desired resources, health
evaluation, and removal behavior. The generic environment reconciler knows
service keys and lifecycle results, not service-specific fields.

### 5.5 Infrastructure claims are below product services

A product database service is not a physical database cluster. It creates a
database claim. A product bucket service is not SeaweedFS. It creates a bucket
claim. Internal platform features may create the same claims without pretending
to be user-facing services.

### 5.6 Explanations do not drive behavior

Runs, steps, logs, activity entries, and UI notifications explain or distribute
state changes. The reconciler never consumes them as its source of truth.
Operations may produce durable domain outputs such as an Artifact or Backup,
but consumers use those verified output records, never parse execution logs or
infer correctness from display status text.

### 5.7 Local development and remote deployments share the compiler

`skali dev` is a local toolbox workflow, not a remote environment named
development. It uses Kubernetes, the same operators, compiler, revision
builder, service modules, and rendering contracts as remote deployment. Local
runtime differences such as ingress hostnames, storage class, architecture,
and capacity are explicit local target capabilities.

### 5.8 Secrets never enter portable or observable data

Definitions contain secret declarations/references. Revisions contain opaque
secret references and versions, never plaintext. Observations, events, run
logs, metrics, and API list payloads must be safe to persist and display.

### 5.9 Headless behavior is the product contract

Every project lifecycle must work through the public API and CLI without the
web UI. The server owns parsing semantics, validation, planning, revision
preparation, deployment state, observation, and operations. The CLI owns the
filesystem, local build executor, disposable development-runtime lifecycle, and
terminal UX, but it cannot invent alternate deployment semantics or administer
production Kubernetes installations.

The web UI later manipulates the same typed definition and calls the same plan,
deploy, operation, and query APIs. A UI-only create/update path is not allowed.

### 5.10 Installation and reconciliation have separate owners

The privileged installer owns host-level K3s state and the bootstrap resources
that make a Skali installation exist. The `skalid` reconciler owns project
resources and explicitly delegated product-platform resources, such as shared
database pools or an object-storage subsystem, but never its own bootstrap
dependencies. Installer-owned bootstrap, `skalid`-owned platform, and
`skalid`-owned project resources use distinct labels, field managers, service
accounts, and prune scopes. A project deploy, environment deletion, or generic
reconciliation audit can never update or delete the Skali control plane that
performs it.

The installer must remain useful when `skalid`, Skali Postgres, the registry,
or the Kubernetes API is degraded. Its diagnosis and host-repair paths cannot
depend on the Skali public API or product database.

## 6. Core domain model

### 6.1 Project definition

The default canonical portable source is:

```text
skali.yml
```

`skali.yaml` is accepted as an alternative. If both default names exist, the
CLI reports ambiguity rather than choosing silently. Every manifest-aware
command accepts `--manifest PATH`, which selects one complete definition and is
independent of the target environment. Skali does not implicitly load
`skali.<environment>.yml`; alternative definitions are explicit inputs.

YAML is the primary human format. JSON is accepted because both parse into the
same schema. Internally the system operates on separate authoring and compiler
types; it does not pass unstructured YAML maps or the wire DTOs into the core.

The pipeline is:

```text
source bytes
  -> parse
  -> strict structural validation
  -> normalize units/defaults/references
  -> semantic and dependency validation
  -> typed, target-independent ProjectDefinition
  -> canonical encoding
  -> content hash
```

Map order, whitespace, YAML aliases, and the original notation must not affect
the canonical hash. The original source may be retained for display or download,
but behavior is defined by the normalized typed form.

A project has one current draft document. Its source mode may be:

- `managed`: edited through the web/API as patches to the same document.
- `file`: replaced by CLI/Git submissions of a selected manifest.

Both modes feed the same draft type. Mutations use optimistic versions/ETags so
a file push cannot silently overwrite web changes and the web cannot silently
overwrite a newer file submission. Switching source mode is explicit.

A complete source project normally has this shape:

```text
project/
  skali.yml
  .env.example
  .env                     # optional local values; normally gitignored
  Dockerfile               # when an application uses a build source
  src/
  ... project source
```

Environment files are optional CLI inputs, not part of the portable definition.
They may live anywhere and are selected explicitly with `--env-file`;
interactive deploys also offer the project root's `.env` and `.env.*` files as
an override selection. Remote environments normally use their independently
stored value space. `.skali/` is
reserved for disposable local tool state rather than the canonical manifest.

Within that reserved space, `.skali/target.yaml` is the checkout binding: it
records where this checkout deploys as the remote master URL, the project
name, and the default environment. The first successful `skali plan` or
`skali deploy` writes it and makes the directory self-ignoring by creating
`.skali/.gitignore` containing `*`; deleting the directory relinks the
checkout on the next run. One checkout binds exactly one target; hosting the
same source twice means two checkouts. The binding stores no secrets and
never travels in the portable definition.

### 6.2 Stable service identity

Every service has a stable key inside the project definition. The key, not a
generated database UUID, is the portable identity. A display name may change
without changing the key. Changing or removing a key is a destructive change
for stateful services and must be shown clearly by `plan`.

Generated cluster names, database identities, bucket names, and Secret names are
runtime allocations derived from `(installation, project, environment, service
key)` or stored as durable outputs. They do not travel between installations in
the portable definition.

### 6.3 Definition structure

The exact alpha syntax needs fixtures before implementation, but the semantic
shape is fixed:

```yaml
version: "1"
name: example

values:
  APP_DOMAIN:
    description: Public domain for the web application.
  API_KEY:
    secret: true

applications:
  web:
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
    environment:
      DATABASE_HOST: "{{databases.data.host}}"
      DATABASE_PASSWORD: "{{databases.data.password}}"
      API_KEY: "${API_KEY}"

databases:
  data:
    engine: postgres
    version: 17
    isolation: project
    availability: single

buckets:
  assets:
    visibility: private
    quotas:
      storage: 20GB

backups:
  daily:
    schedule: "0 3 * * *"
    retention: 7d
    include:
      databases: all
      buckets: all
      volumes: all
```

This is illustrative, not a frozen field spelling. The design rules are frozen:

- Application, database, and bucket keys are stable identities in separate
  top-level authoring collections. The compiler normalizes them into typed
  resource identities internally.
- `${NAME}` and `${NAME:-default}` reference the selected environment's project
  value space. The compiler derives requirements from their use.
- The optional top-level `values` collection declares metadata for project
  values: `secret` and `description`. Existence, required-ness, and defaults
  still derive from use; secrecy comes only from this declaration. A declared
  value that is never referenced is an error.
- A secret value cannot carry an inline default and may only be referenced
  where the rendered position is secret-capable; initially that is application
  environment variables.
- `{{databases.<key>.<output>}}` and `{{buckets.<key>.<output>}}` are parsed into
  typed output references and dependencies; they are not runtime string
  templates.
- The environment value store distinguishes secret and non-secret values.
  Revisions contain secret handles and versions, never plaintext.
- An application chooses exactly one of `image` or `build`.
- Build paths are relative to the project root and cannot escape it without an
  explicit opt-in.
- Product concepts are represented; raw Kubernetes YAML is not embedded.
- Unknown fields are rejected for the active schema version.
- The definition has a simple explicit schema `version` from its first release.
- Skali accepts public units such as `GB`, `GiB`, and decimal CPU cores, then
  converts them into exact internal quantities before backend rendering.

### 6.4 Environment values

Portable structure and environment-specific input are separate:

```text
ProjectDefinition + EnvironmentValues + Artifacts + TargetCapabilities -> Revision
```

An environment is a remote copy of a project inside one Skali installation. In
the initial single-cluster product, environments share the installation's
cluster while retaining independent namespaces, values/secrets, target and
active revisions, runtime allocations, release history, and backups.

Environment values include:

- Domains and public endpoints.
- Plain environment-specific configuration.
- Secret bindings.
- Plain and secret application configuration referenced through `${...}`.

Environment selection never selects a manifest automatically. A deployment
uses the explicitly selected definition (or `skali.yml` by default) and the
selected remote environment's value space. Different definitions may be
selected with `--manifest`, but they are complete alternative inputs rather
than implicit environment merge patches.

The CLI accepts dotenv-style files as the initial ergonomic input format:

```text
.env
```

Required keys and defaults are derived from `${NAME}` and
`${NAME:-default}` expressions in the compiled definition. Missing required
keys fail planning. Unknown keys are rejected or require an explicit ignore
flag. Secrecy comes from the manifest's `values` declarations and is recorded
by the remote environment store; the dotenv format itself never decides
secrecy. The accepted dotenv dialect is the godotenv-compatible subset the
CLI already implements.

An environment file may contain both plain and secret values. When uploaded,
`skalid` separates them: plain typed values enter `EnvironmentValues`; secret
values enter the installation's secret store and are represented elsewhere only
by opaque references and versions. The original file is never persisted as a
blob and values are redacted from plans and run logs.

Remote environments can use either values already stored in Skali or values
selected from a local file for the current deployment. Non-interactive/CI use
must choose explicitly, so a deployment never uploads local secrets
accidentally. `skali dev` may use an explicitly selected local env file, but it
does not create or select a remote development environment.

Environment files should normally be ignored by Git when they contain
machine-local or secret values. A checked-in `.env.example` or non-secret values
file may document required inputs.

### 6.5 Revision

A deployment prepares an immutable revision containing:

- Canonical project definition and schema version.
- Definition content hash.
- Environment-values hash and non-secret resolved values.
- Opaque secret references/versions.
- Build-context/source hashes and artifact provenance.
- Managed-registry image names and digests.
- Original upstream image references where applicable.
- Compiler and service-module versions.
- Required target capabilities.
- Prepared, typed service specs.
- Dependency graph.
- A revision checksum.

Operator versions and blessed runtime bundle versions are recorded in the
revision or installation profile so reproducibility is explainable. The same
definition and values should generate the same revision checksum when resolved
against the same artifact and runtime versions.

Required target capabilities are derived from the definition: applications
require `application` nodes, databases require `database` nodes, buckets
require `object-storage` nodes, and routes require `edge` nodes. A revision is
checkable against an installation's recorded layout before deployment begins.

### 6.6 Environment target

Each environment has one target revision pointer and, separately, its last
active revision pointer. Deploying changes the target. Reconciliation makes the
cluster approach it. Activation occurs only after the revision's required
health conditions pass.

- Rollback sets the target to a previous immutable revision.
- Cancellation marks the run cancelled and returns the target to the prior
  active revision if the new revision has not activated.
- A daemon restart does not alter either pointer.
- Background drift repair continues toward the active target without creating a
  new revision.

### 6.7 Common service envelope, type-specific models

V2 should not force every service into one database table or one giant union.
The common product envelope contains only universally meaningful fields:

- Stable service key.
- Type identifier.
- Display metadata.
- Dependency references.
- Lifecycle ownership.

The application, database, and object-storage modules keep typed specs and typed
prepared forms. Internally they may use separate tables and packages. The common
contract is lifecycle behavior, not identical storage.

Each service module supplies the equivalent of:

```text
decode + normalize + validate
prepare external artifacts
declare dependencies and outputs
produce desired resources or infrastructure claims
evaluate health from observed state
describe removal and data-loss consequences
contribute execution steps and diagnostics
```

The architecture is accepted only if adding a fourth service kind does not
require modifying the generic deployment state machine or environment
reconciler.

## 7. Observed-state system

### 7.1 Goal

Skali should answer normal state and topology questions from its maintained
model, not by asking Kubernetes during each API request. For an application
with three replicas, it should already know:

- Which three pods belong to the application and revision.
- Which nodes host them.
- Each pod's phase, readiness, conditions, restarts, and container state.
- Deployment generation and rollout state.
- Relevant Kubernetes warning events.
- Whether the observation source is current, reconnecting, or stale.

### 7.2 Observed store

Introduce an `ObservedStore` abstraction with typed indexes for:

- Kubernetes objects by GVK/namespace/name/UID.
- Ownership labels and owner references.
- Project/environment/service/revision identity.
- Node placement.
- Conditions and timestamps.
- Provider-specific observations such as CNPG topology and SeaweedFS state.
- Platform-subsystem observations such as registry capacity/health and builder
  availability.
- Source health, last successful sync, and resource version/cursor.

The production implementation is an in-memory materialized view fed by
watchers and provider observers. It is rebuilt on startup. Current pod objects
are not persisted as authoritative database rows.

Durable history, metrics, and transitions are stored separately. This prevents
stale persisted pod rows from being mistaken for current truth.

### 7.3 Kubernetes observation

- Initial LIST for every required kind.
- Wait for cache synchronization before declaring the cluster view ready.
- WATCH from the returned resource versions.
- Watch core kinds needed for topology and health: Nodes, Namespaces, Pods,
  Deployments, StatefulSets where used, Services, Ingresses, PVCs, Jobs, and
  Kubernetes Events.
- Dynamic watches for blessed operator CRDs such as CNPG resources.
- Index every managed object back to its installation, environment, service,
  and revision using a stable label contract.
- Convert each cache change into an affected-owner queue entry.
- Periodic resync and a slower full reconciliation audit as backstops.

### 7.4 External-system observation

Systems without a useful watch API, initially SeaweedFS and the managed registry,
use provider observers that update the same `ObservedStore` contract with an
explicit freshness timestamp. Their poll frequency may differ, but consumers
see one consistent fresh/stale/unknown model.

Request-time provider reads are limited to explicit sensitive or streaming
operations such as credential reveal, exec, or raw log attachment. Product
status pages never depend on request-time cluster calls.

### 7.5 Health projections

Health evaluation is pure over prepared intent plus observation:

```text
Evaluate(prepared service, observed resources) -> Health + Diagnostics
```

Diagnostics are structured, for example:

- `replicas_ready: 2/3`
- `pod: web-abc`, `reason: ReadinessProbeFailed`
- `node: worker-2`, `reason: NotReady`
- `observation_stale_since: ...`

The UI can render product-level health while still allowing a user to inspect
the underlying members.

The responsiveness objective should be measurable: in a healthy local cluster,
a pod readiness or deletion event should normally be reflected in the Skali API
and subscribed UI within two seconds. This is a target for tests and profiling,
not a promise that distributed systems have zero latency.

## 8. Reconciliation kernel

### 8.1 Work loop

```text
definition/value change
  -> validate candidate and plan
  -> build/import/verify artifacts
  -> prepare immutable revision
  -> set environment target
  -> enqueue environment

cluster/provider watch change
  -> update ObservedStore
  -> resolve affected owners
  -> enqueue environment/service/subsystem

worker
  -> load target revision
  -> evaluate dependencies
  -> plan desired resources/claims
  -> apply idempotently
  -> evaluate observation
  -> update projections and run steps
  -> requeue only when additional work or observation is required
```

There is still a periodic audit, but there is no global short-interval scan as
the primary responsiveness mechanism.

### 8.2 Desired-resource planning

Service modules should produce typed desired resources or claims. Kubernetes
rendering is deterministic and side-effect free. Application modules produce
Kubernetes objects. Database and object-storage services primarily produce
lower-level claims, whose subsystem drivers produce Kubernetes/operator objects
and external ensure actions.

Apply and prune remain generic:

- Server-side apply with a stable field manager.
- Stable ownership labels.
- Prune only objects owned by the exact logical resource and absent from its
  desired set.
- Stateful data deletion requires an explicit destructive target transition;
  absence caused by a compiler error never prunes data.

### 8.3 Dependencies

Dependencies form a directed graph in the prepared revision. Examples:

- Application waits for database connection outputs.
- Application waits for bucket connection outputs.
- SeaweedFS filer waits for its system database claim.
- A database tenant waits for a healthy physical database cluster.

The reconciler may work independent branches concurrently. A dependency that is
not ready produces a visible waiting step rather than an opaque retry.

### 8.4 Status and failure

- Provider transport failure: observation becomes stale/unknown and retry is
  scheduled.
- Invalid definition: revision preparation fails before target mutation.
- Apply failure: run step records the diagnostic; target remains and retries
  according to policy unless terminal.
- Rollout health failure: deployment run fails and policy determines whether to
  retain the failed target for inspection or return to the previous active
  target. The initial product policy remains automatic fallback.
- Out-of-band deletion: watch update enqueues the owner and reconciliation heals
  it.
- Deletion: explicit desired absence, with a persisted destructive decision and
  service-specific teardown steps.

## 9. Execution journal and observability

### 9.1 Separate signal types

V2 uses separate systems for:

1. **Observed state:** current truth projection.
2. **Health transitions/activity:** durable facts such as became degraded,
   recovered, user changed settings, or credentials rotated.
3. **Execution runs:** detailed progress and diagnostics for deployments,
   restores, destructive removals, platform bootstrap, and other operations.
4. **Runtime logs:** application/container stdout and stderr.
5. **Client invalidation:** lightweight notification that a query projection
   changed.

These may share transport or storage utilities, but they are different domain
models.

### 9.2 Run model

```text
Run
  id, kind, target, actor, status, timestamps
  Step[]
    id, parent_id, key, title, status, progress, timestamps
    Attempt[]
      id, number, status, timestamps
      LogEntry[]
        timestamp, level, message, structured fields
```

Step keys are deterministic so a restarted controller can reattach to or
reconstruct the same logical step. Log entries are append-only, bounded, and
redacted.

Statuses form small closed machines:

```text
Run     pending -> running | cancelled
        running -> succeeded | failed | cancelled

Step    pending -> waiting | running | skipped | cancelled
        waiting -> running | skipped | failed | cancelled
        running -> waiting | succeeded | failed | cancelled

Attempt running -> succeeded | failed | cancelled
```

- `waiting` is the visible dependency state from the reconciliation kernel: a
  step that cannot proceed names what it is waiting for instead of retrying
  opaquely, and a policy timeout fails it.
- Retries stay inside one step. An attempt exists only once it has started, a
  new attempt starts only after the previous one is terminal, and the step
  stays `running` across attempts until it succeeds or retry policy declares
  it failed.
- Parent steps aggregate children: succeeded when every child succeeded or was
  skipped, failed or cancelled when a child is.
- A run reaching a terminal status forces every non-terminal step and attempt
  terminal: running work is cancelled or failed, unstarted work is skipped.
- A second operation queued behind an environment's single in-flight rollout
  is a `pending` run.
- On controller restart, an attempt whose executor no longer exists is failed
  with a restart diagnostic; the deterministic step key lets the controller
  reattach and continue the same run toward the unchanged target.

A deployment should read approximately like:

```text
Deploy project example to production
  Validate project definition
  Prepare environment values
    Import .env.production: 5 plain, 3 secret values
  Prepare artifacts
    web
      Build locally
      Push registry.example/skali/example/web
      Verify sha256:...
    worker
      Import ghcr.io/example/worker:1.4
      Cache and verify sha256:...
  Create immutable revision 01...
  Prepare environment
  Provision dependencies
    postgres
      Select database cluster
      Create logical database and role
      Publish connection output
    assets
      Ensure bucket
      Publish S3 connection output
  Apply applications
    web
      Apply Deployment, Service, and Ingress
      Wait for rollout: 2/3 ready
      web-abc: readiness probe failed with HTTP 503
  Verify revision health
  Activate revision
```

The run is an explanation over reconciliation. Deleting all run rows must not
change the target or stop the controller from recovering.

### 9.3 Runtime logs

Initial application logs should provide:

- Live follow by service, pod, and container.
- Previous-container logs after restarts when Kubernetes retains them.
- A merged service stream with member labels.
- CLI and web streaming through the public Skali API.

Retention/aggregation beyond Kubernetes' local logs is a separate policy and
can begin with live streaming only. If durable retention lands later, it is
its own platform subsystem with its own storage; runtime logs never enter the
system database. Deployment-step logs and application logs must never be
combined.

### 9.4 Activity feed

Retain a durable product activity feed for meaningful transitions and user
actions. It should link to runs where relevant, but it should not duplicate
every run log line or every Kubernetes event.

## 10. Service systems

### 10.1 Application module

Initial definition capabilities:

- Image source by reference and digest resolution.
- Build source with project-relative context, Dockerfile/build configuration,
  target platform, and safe build inputs.
- Main process/port, with a schema that can later expand to multiple processes.
- Fixed replica count and optional autoscaling range.
- CPU, memory, and ephemeral-storage policy.
- Readiness/liveness health checks.
- Plain values, typed service outputs, and secret references.
- Routes/domains and TLS policy.
- Persistent-volume claims with explicit portability/replica constraints.
- Database and object-storage bindings.

Initial runtime capabilities:

- Deployment/Service/Ingress/PVC planning.
- Managed-registry, digest-pinned pods regardless of whether the source was an
  imported image, local build, or cloud build.
- Volume-backed applications initially use one replica and `Recreate` rollout;
  rolling replacement remains unavailable until the selected storage
  capability safely supports concurrent members.
- Member-level health and node placement from `ObservedStore`.
- Start/stop/restart expressed as explicit runtime intent or operations, not
  hidden changes outside the model.
- Live logs and metrics.
- Clear rollout steps and diagnostics.

Later module extensions:

- Multiple processes, workers, sidecars, cron jobs.
- Git/remote builders and buildpacks.
- RWX volumes and advanced storage classes.
- Network/egress policies and custom health commands.

### 10.2 Database substrate

The database subsystem is below project services and platform subsystems. Its
core concepts are:

#### DatabaseClaim

Requested database capability:

- Owner reference: a project service or a system component.
- Engine and major version.
- Isolation policy: shared, owner/project, or dedicated.
- Availability/durability policy.
- Capacity and storage policy.
- Extensions/features.
- Backup policy.
- Stable logical claim key.

#### DatabaseCluster

A physical database runtime, initially a CNPG `Cluster`:

- Engine/version.
- Instance count and replication configuration.
- Compute and storage envelope.
- Placement/capability requirements.
- Accepted claim classes and capacity state.
- Health, primary/replica topology, backup status, and upgrade state.

#### DatabaseTenant

A logical database inside a cluster:

- Claim and cluster identity.
- Generated database/role identity.
- Extension state.
- Credential Secret reference.
- Connection outputs.
- Tenant health and provisioning status.

#### DatabasePlacement

System-owned assignment from claim to cluster. It is not part of the portable
project definition or immutable revision because the platform may relocate a
tenant without changing user intent.

#### Claim lifecycle

A claim moves through durable phases. Transient progress and failure detail
belong to runs and health diagnostics, never to the phase:

```text
pending -> bound | released
bound -> provisioned | pending | releasing
provisioned -> releasing
releasing -> released
```

- `pending`: recorded, no placement. An unsatisfiable claim stays pending with
  a visible waiting condition; there is no failed phase because
  level-triggered reconciliation keeps working toward the phase's goal.
- `bound`: a placement assigns the claim to a cluster; tenant provisioning and
  repair happen here. Revoking a placement before provisioning returns the
  claim to pending.
- `provisioned`: tenant identity, credential reference, and connection outputs
  are durably recorded, and consumers may bind. Relocation supersedes the
  placement without changing the phase.
- `releasing`: a persisted destructive decision started teardown. The
  transition is one-way because data may already be destroyed.
- `released`: teardown finished; identity is retained for history.

A claim deleted before it was ever placed releases directly
(`pending -> released`). Bucket claims are expected to reuse the same phase
machine. The bootstrap control-plane database has no claim and no phase; the
substrate cannot select it.

#### Database engine driver

An engine driver owns physical-cluster planning, tenant/role planning, engine-
specific observation, connection-output construction, backups, and safe
upgrade/relocation procedures. PostgreSQL/CNPG is the only initial driver.

### 10.3 User and system database consumers

A user-facing database service prepares a `DatabaseClaim` owned by:

```text
project/<project>/environment/<environment>/service/<key>
```

The SeaweedFS metadata dependency prepares a claim owned by:

```text
system/object-storage/metadata
```

Both claims travel through the same placement, CNPG cluster, logical tenant,
credential, observation, backup, and diagnostic code. Differences are policy:

- System claims may require dedicated placement and stricter durability.
- System claims cannot be deleted through project operations.
- System connection outputs are consumed by another subsystem instead of an
  application.
- System health rolls up on the platform page rather than a project page.

There must not be a separate `ensureObjectMetaPool` provisioning path in v2.
The object-storage subsystem simply owns a database claim and depends on its
outputs.

System claims are placed on `skalid`-owned database clusters in the platform
scope, normally the shared pool created during installation initialization.
They are never placed in the bootstrap control-plane database: `skalid` state
is not a claim, the bootstrap cluster accepts no tenants, and the claim
substrate cannot select it. Everything whose size or load grows with user
activity lives in clusters `skalid` fully manages, while the installer-owned
bootstrap surface stays small and quiet.

### 10.4 Database service product surface

The initial user-facing database service retains:

- PostgreSQL engine/version selection.
- Shared/project/dedicated isolation intent.
- Single/async/sync availability intent where supported.
- Extensions.
- Managed credentials and application connection output.
- Instance/topology view.
- Metrics and health.

Before the v2 stable release it should also have:

- Scheduled backups to external object storage.
- Manual backup and restore run types.
- A tested restore path, not merely successful backup creation.
- Safe credential rotation.

External connectivity, more engines, and automated relocation may follow after
the core claim/placement model proves stable.

### 10.5 Object-storage substrate and service

Separate the physical object-storage system from logical bucket services:

- `ObjectStoreCluster`: SeaweedFS masters, filer, gateways, volume servers,
  topology, replication, and capability requirements.
- `BucketClaim`: owner, quota, policy, and stable key.
- `BucketAllocation`: generated bucket identity and credential Secret.
- `BucketOutput`: internal/external endpoint, bucket, region, and secret refs.
- `ObjectStoreDriver`: physical planning, external ensures, observation, and
  teardown.

The physical SeaweedFS system owns the system database claim described above.
The dependency graph becomes:

```text
database substrate healthy
  -> object-storage metadata tenant ready
  -> SeaweedFS filer ready
  -> S3 gateway ready
  -> bucket claims ready
  -> connected applications ready
```

The initial bucket product surface retains quotas, credentials, internal
connections, an optional external endpoint, usage, member health, and safe
deletion. Erasure coding, lifecycle rules, public access, and evacuation remain
later policies built on the same substrate.

### 10.6 Build, artifact, and managed-registry subsystem

Build execution and artifact storage are platform systems below application
services. An application chooses one source form:

- `image`: an existing OCI image reference.
- `build`: a project-relative build context and build configuration.

Both forms must resolve to the same prepared output before revision creation:

```text
Artifact
  managed registry reference
  manifest/index digest
  platform(s)
  provenance
  source or upstream digest
  build-context hash when built
```

#### Build execution

A build source may select an executor at deploy time:

- `local`: the CLI invokes the supported local builder, streams progress, and
  pushes the result directly to the target Skali registry.
- `cloud`: the CLI uploads an allowed build context or references a source
  snapshot; a Skali-managed builder runs BuildKit-compatible work and pushes to
  the same registry.

The manifest describes how to build. The deploy command chooses where to build
unless installation/project policy fixes it. Executor choice must not change the
resulting application or reconciliation model.

Build arguments are divided into ordinary and secret values. Secret build
inputs use a secret mount mechanism, never Dockerfile `ARG` persistence, and are
redacted from context metadata, cache keys where necessary, and logs.

Build-context collection honors an explicit ignore file and applies hard safety
exclusions for VCS internals, selected env files, and common `.env` files unless a
future narrowly scoped feature says otherwise. Environment values enter the
runtime/secret-input pipeline; they are not accidentally uploaded as ordinary
source files to a cloud builder.

Each build is an operation with detailed steps and logs. On success it creates
a durable Artifact record. Revision preparation consumes that record; it never
infers an image digest by reading build logs or run status text.

#### Managed OCI registry

Every Skali installation manages an OCI registry as an installation subsystem.
It serves three roles:

1. Upload destination for locally built images.
2. Upload destination for cloud-built images.
3. Installation-local cache/import store for externally referenced images.

For an external image, preparation resolves the upstream reference and imports
the content into a deterministic cache namespace in the managed registry. The
revision records both the original reference/provenance and the managed digest.
Cluster workloads pull the managed reference, so a later upstream outage or tag
movement cannot affect an existing revision.

The registry is a standard OCI distribution service managed by Skali, not a new
registry protocol implemented by `skalid`. Authentication, scoped push/pull
credentials, health, capacity, garbage collection, and retention are surfaced
through Skali.

Artifact classes have different durability policy:

- Imported upstream content is a reconstructable cache and may be evicted when
  no retained revision references it.
- Locally/cloud-built release artifacts may be the only deployable copy and are
  retained while referenced by a revision plus a configured safety window.
- The local-development registry is disposable after local revisions and source
  can be rebuilt, unless the user explicitly retains it.

Production registry storage belongs to the installer-owned system boundary. The
registry service may run inside the cluster, but its durable data uses a
bootstrap-owned volume or an independent external object store and off-cluster
backup policy. It must not depend on the ordinary project object-storage system
that it may be required to bootstrap.

The registry itself is observed like every platform subsystem. Capacity,
availability, import/build failures, and artifact retention appear in platform
health and operation runs.

#### Registry decision

The managed registry is CNCF Distribution (the OCI reference registry),
deployed by the installer as a single instance on a registry-capable node:

- Storage uses the filesystem driver on an installer-owned volume by default;
  a supported installation may configure the S3 driver against an external
  object store instead. Neither changes the artifact contract.
- Authentication uses the registry token protocol. The installer generates
  the token-signing keypair at initialization: the registry trusts the
  certificate, `skalid` holds the signing key. Token verification is offline,
  so the registry never calls `skalid` while serving a request.
- Push credentials are short-lived JWTs minted by `skalid`, scoped to exactly
  one repository for one build or import. Pull access uses a longer-lived
  read-only token that `skalid` refreshes and the installer places into
  containerd registry configuration; pulls therefore keep working through
  control-plane downtime until that token expires, and the embedded registry
  mirror covers already-pulled images regardless.
- Repositories are laid out deterministically: release artifacts under
  `skali/<project>/<application>`, imported upstream content under
  `cache/<host>/<path>`. A push token authorizes one such repository.
- Retention is `skalid`-owned: revisions lease their artifacts, `skalid`
  deletes unreferenced manifests through the registry API, and blob space is
  reclaimed in scheduled read-only garbage-collection windows. Read-only mode
  still serves pulls; only pushes pause, so a GC window affects deploys and
  nothing else.
- Any transparent pull-through or mirroring feature stays disabled: external
  content enters only through explicit imports that create verified Artifact
  records with provenance.

#### Artifact lifecycle

An artifact record moves through durable phases; the build or import run
carries progress and failure detail:

```text
pending -> verified | abandoned
verified -> evicted
```

- `pending`: the expected content of one build or import; a scoped push
  credential may exist for it.
- `verified`: the manifest and digest are confirmed present in the managed
  registry. Only verified artifacts can enter a revision.
- `abandoned`: the build or import ended without verified content, and
  partial uploads are cleaned up. There is no failed phase on the artifact;
  the run failed, the record is abandoned.
- `evicted`: previously verified content was reclaimed by retention policy.
  The record and its provenance remain for history; re-importing the same
  upstream creates a new artifact record.

An artifact referenced by a retained revision cannot be evicted, and release
artifacts additionally hold a configured safety window. Cache artifacts are
evictable whenever unreferenced.

## 11. Local development and CLI

### 11.1 Decision: local instance, shared core

The local development cluster is a distinct local Skali installation. It is not
registered as another cluster inside a production control plane and does not
require production credentials or network access.

It nevertheless uses the same architecture:

- The same `skalid` binary and migrations.
- The same Postgres persistence model.
- The same project-definition parser and revision builder.
- The same Kubernetes and operator bundle.
- The same service modules and database/object-storage subsystems.
- The same observed-state watchers.
- The same reconciliation and run-journal code.

Only the local target profile and locally selected values differ. This local
state is not represented as a remote project environment.

This gives offline use, safe resets, reproducible behavior, and high parity
without making production responsible for a developer's laptop.

### 11.2 Local topology

The v1 local implementation is CLI-managed:

```text
host OS
  skali CLI
  disposable container/VM runtime
    k3d cluster using a pinned k3s version
      skali-system
        skalid
        Skali Postgres
        local OCI registry
        blessed operators and runtime components
      project workloads
```

The CLI owns the disposable development-cluster lifecycle and applies the same
installer-owned system bundle used in production with a lightweight local
profile. It discovers the in-cluster API through an explicit loopback route or
port-forward. Stopping and starting the local cluster retains its volumes;
`skali dev reset` intentionally destroys the complete local installation unless
the user first exports it.

The local exposure convention is fixed: the local edge publishes HTTP on host
port 8080, routes use `*.localhost` names, which resolve to loopback without
configuration, and the local Skali API/UI is served through the same edge at
`skali.localhost`. The local platform is HTTP-only by decision (2026-07-20):
a self-signed local HTTPS endpoint tests nothing a client would trust, so
TLS issuance and cert-manager are production-bundle concerns, `tls:
automatic` in a manifest is inert locally, and the parity difference is
stated instead of simulated (section 11.7). The local managed registry is
published on a dedicated loopback port for host-side pushes and under its
stable in-cluster name for pulls. `skali dev` prints the exact URLs it
provisions.

Docker/k3d is the first runtime because the repository already uses it. A future
Lima-backed provider may implement the same CLI-owned local-runtime contract for
machines that should not depend on Docker Desktop. That is a provider for local
machine lifecycle, not a second Skali deployment backend.

### 11.3 Headless deployment workflow

A complete custom project consists of three independently meaningful inputs:

1. `skali.yml`/`skali.yaml`, or `--manifest PATH`: portable services and
   build/runtime structure.
2. A selected environment file or values already stored on the target
   installation.
3. Project source directories for services using `build`; projects using only
   existing images need no source tree beyond the manifest.

The primary remote workflow is project-wide:

```sh
# Build on this machine, upload selected values, push artifacts, and deploy.
skali deploy --environment production --build=local \
  --env-file ./.env.production

# Send build work to a managed builder, but keep the same manifest and deploy.
skali deploy --environment production --build=cloud \
  --env-file ./.env.production

# Deploy using the values already stored for the remote environment
# (the default when no env file is passed).
skali deploy --environment production --build=auto
```

`--build=auto` follows project/installation policy and may choose a configured
cloud builder or local fallback. The executor applies only to `build` sources;
`image` sources are imported into the managed registry.

The environment's stored values are the default source. Interactive use asks
for the environment when the flag is omitted (listing the project's
environments), discovers the project root's `.env` and `.env.*` files, and
offers them as an explicit override selection; declining keeps the stored
values. The selection must show the path, target installation, project, and
environment, without printing values. Non-interactive use must name the
environment, either with `--environment` or through the checkout binding, and
uploads only with an explicit `--env-file`; without one the stored values
apply. Skali never uploads a file that was not explicitly selected.

The checkout binding (`.skali/target.yaml`, section 6.1) selects the deploy
target. Its master URL wins over the machine's current remote and is resolved
among the machine's configured remotes by master URL, never by remote name;
without a matching remote the command fails and points at `skali remote add`.
The bound environment is the default for interactive and non-interactive use
alike, and `--environment` overrides it for one invocation without rewriting
the binding. The manifest `name:` remains the project identity and must equal
the bound project name; a mismatch is an error, never a rename. `skali dev`
neither reads nor writes the binding. Writing the binding is local tool
state, not an installation mutation: both `skali plan` and `skali deploy`
record it once the project and environment resolve, except against the
dev-owned `local` remote, which is never bound.

Only interactive `skali deploy` may create a missing project or environment
on the installation, each behind an explicit `[y/N]` confirmation; when a
freshly created project has no environments, the environment name is prompted
with `production` as the default. `skali plan` never mutates the
installation: with a missing project or environment it fails and points at
`skali deploy`. Non-interactive deploys never create.

The deploy workflow is:

1. Discover and parse the manifest locally for immediate feedback.
2. Connect to the selected Skali remote and submit the candidate definition.
3. Select remote environment values or parse and securely upload the chosen env
   file as candidate values.
4. Have `skalid` independently validate the definition/values and return a
   preliminary semantic and destructive plan.
5. Confirm the plan in interactive use, or require an explicit CI approval flag.
6. Start one user-visible deployment-preparation run.
7. For each application, import its image or execute its build locally/cloud.
8. Push/verify every result in the target installation's managed registry and
   create Artifact records.
9. Prepare the immutable revision from the candidate definition, candidate
   values, target capabilities, and Artifact digests.
10. Atomically promote the submitted definition/value versions as appropriate,
    set the environment target, and begin reconciliation.
11. Attach the terminal to the run tree through activation, failure, or detach.

Candidate plain/secret values may be staged during preparation, but a build or
import failure must not change the environment's deployed values or target.
Unused staged secret versions are garbage-collected without ever appearing in
logs.

Local builds and cloud builds appear as child steps of the same deployment run.
For a local executor, the authenticated CLI streams structured progress to the
run while it builds and pushes. The server verifies the resulting registry
manifest and digest before creating the Artifact; client-reported success is
not trusted as deployment state.

### 11.4 `skali dev` experience

Running this inside or below a directory containing `skali.yml` or
`skali.yaml`:

```sh
skali dev
```

should:

1. Find the project root and manifest.
2. Use `--env-file` when given; otherwise use `./.env` automatically when
   present, announcing the selection. Local values never leave the machine,
   and local development does not require or create a remote environment.
3. Validate the manifest and local values.
4. Ensure the k3d cluster and its in-cluster `skali-system` profile (`skalid`,
   Postgres, registry, and blessed operators) are running.
5. Build build-sourced applications locally with the required target
   architecture and import image-sourced applications.
6. Push/verify all artifacts in the local managed registry.
7. Submit the definition and dev values to the local Skali instance.
8. Prepare and target a local revision.
9. Attach the terminal to the deployment run.
10. Print routes and connection information once ready.
11. Continue showing health changes and concise runtime logs until detached.

Bare `skali dev` is the paved path. Supporting lifecycle commands should include
approximately:

```text
skali dev                 ensure, build, deploy, and attach current project
skali dev --env-file PATH deploy with an explicitly selected local env file
skali dev up              ensure the local platform only
skali dev status          show platform and current-project state
skali dev logs [service]  stream runtime logs
skali dev exec <service>  execute in a selected application member
skali dev open [service]  open a local route
skali dev stop            stop local services but retain state
skali dev reset           destructively recreate local state and cluster
```

Exact spelling can change during CLI UX prototyping, but the workflow ownership
is decided.

### 11.5 File watching

The first release may require rerunning `skali dev` after source changes, but the
architecture should support an attached watch mode:

- Manifest/value change: revalidate, show plan, prepare a revision.
- Source change for a build service: rebuild only the affected image, prepare a
  new local revision, and reconcile it.
- No change: do not create meaningless revisions.

Hot module reload is framework-specific and remains an application concern.
Skali's generic behavior is build-and-redeploy.

### 11.6 CLI responsibilities

The CLI is not an endpoint mirror. It owns workflows requiring filesystem,
terminal, build-engine, or local-machine access:

- Discover and validate project definitions.
- Discover, validate, and securely import selected environment files.
- Explain plans and destructive changes.
- Select local or cloud build execution, build/import images, and push artifacts
  to the target installation's registry.
- Manage the local development installation.
- Attach to run steps and logs.
- Stream application logs and execute into application members.
- Manage named remotes and their authentication through the single
  `skali remote` group.
- Maintain the per-checkout deploy-target binding in `.skali/`.
- Submit definitions/revisions through the public Skali API.

Named remotes are managed by the `skali remote` group. `skali remote add <url>`
creates the remote, named after the URL host by default, and performs the
initial login; `skali remote login` re-authenticates an existing remote. The
`local` remote is owned exclusively by `skali dev`.

Production K3s creation, node lifecycle, Kubernetes upgrades, diagnosis,
repair, and uninstall belong exclusively to the `skali cluster` command group,
backed by the installer engine, which never depends on the Skali API or
product database. Outside `skali cluster`, the CLI does not become a kubectl
wrapper, follow kubeconfig for infrastructure mutation, or access Skali's
Postgres directly; the authority separation is a command-group and package
boundary rather than a binary boundary. The developer-workflow commands' local
lifecycle authority is limited to disposable, user-owned development
installations.

Likely remote workflows are:

```text
skali plan [--environment <name>] [--env-file <path>]
skali deploy [--environment <name>] --build=<local|cloud|auto>
             [--env-file <path>]
skali status --environment <name>
skali runs
skali run show <run>
skali logs <service>
```

They should be designed around user tasks, not generated from the OpenAPI route
list.

### 11.7 Local parity boundaries

Local and production cannot be physically identical. Differences must be
explicit:

- Laptop CPU architecture may differ from production.
- Local ingress and DNS use `.localhost`/port mappings rather than public DNS.
- Storage classes and failure domains differ.
- A single-node local database cannot demonstrate real failover.
- Local capacity is smaller.

`skali dev` should show unsupported guarantees clearly. It must still use the
same project definition, revision format, Kubernetes objects, operators, health
evaluation, and connection-output model.

## 12. API and web responsibilities

### 12.1 API

The API exposes commands and projections rather than leaking storage tables:

- Submit/patch/fetch a project draft with optimistic versioning.
- Submit candidate environment values as typed plain values plus write-only
  secret values.
- Validate and plan a definition/value candidate against an environment.
- Create build/import requests, issue scoped registry upload credentials, and
  verify resulting artifacts.
- Select local/cloud build policy without exposing private builder APIs.
- Prepare/deploy/cancel/rollback revisions.
- Read environments, targets, active revisions, health, topology, outputs, and
  run trees.
- Stream run logs, application logs, and projection invalidations.
- Perform explicit operations such as credential rotation, backup, restore, and
  destructive deletion.

Normal read endpoints consume database projections and `ObservedStore`; they do
not perform live Kubernetes queries.

### 12.2 Web UI

The web UI is deliberately implemented after the headless application slice is
usable through API and CLI. Retain the existing product shape and reusable
components, but bind them to the new model:

- Project definition editor/forms manipulate one typed draft.
- Environment pages show source/version, pending definition/value changes,
  target revision, active revision, and health.
- Deployment pages are run viewers with nested steps, attempts, progress, and
  logs.
- Service pages show typed configuration plus live observed members.
- System pages are projections over platform claims and observed resources.
- Activity is meaningful transition history, not the deployment debug log.

The UI may provide forms rather than exposing YAML directly, but both must edit
or submit the same semantic definition. UI forms may create document patches;
they may not call a parallel create-application/create-database business path
with different defaults or validation.

## 13. Persistence outline

Exact SQL follows domain fixtures, but the model likely needs these durable
categories:

- Auth/user/session tables retained from migrations `00001-00003`.
- Projects and environments.
- Definition sources/drafts with schema and optimistic version.
- Environment values and secret bindings.
- Candidate deployment inputs whose promotion is atomic with target creation.
- Builds, artifacts, provenance, managed-registry references, and retention
  leases from revisions.
- Immutable revisions and prepared service documents.
- Environment target/active revision pointers.
- Runs, steps, attempts, and bounded log entries.
- Durable activity transitions.
- Runtime allocations/outputs for stable database, bucket, and secret identity.
- Database claims, physical clusters, tenants, placements, and backup records.
- Object-store systems, bucket claims, and allocations.
- Metrics/history where retention is desired.

Do not create authoritative pod/deployment/node mirror tables. Live cluster
objects belong to `ObservedStore`. Persisted health summaries, if used for
startup UX, must be marked stale until the initial watch sync completes.

## 14. Installation and production topology

### 14.1 Operational roles

V2 separates developer workflow, privileged installation, and continuous
reconciliation:

- `skali` is the developer CLI. It owns project files, local builds, terminal
  UX, named Skali remotes, and disposable `skali dev` installations. It also
  carries the administrator and recovery role as the `skali cluster` command
  group, which owns host-level K3s installation, joining a host as a server or
  agent, Kubernetes upgrades, diagnostics, repair, uninstall, and the
  installer-owned Skali system bundle.
- `skalid` is the in-cluster control plane. It owns the public API, product
  state, observation, revision targeting, and project reconciliation.

`skali cluster` is interactive by default and detects whether the current host
is fresh, a Skali-managed K3s server/agent, an unmanaged K3s host, or a damaged
installation. Re-running it offers operations appropriate to that state, such
as upgrade, diagnosis, repair, configuration change, restore, or uninstall.
The same engine must also accept an explicit, non-interactive configuration for
cloud-init, configuration management, and CI.

On a Mac, `skali cluster` provisions its own dependencies rather than
instructing the operator to install them: a pinned, checksum-verified Lima
release into a rootless user prefix (an existing `limactl` on PATH, for
example from Homebrew, is detected and used instead), a pinned,
checksum-verified socket_vmnet plus the Lima sudoers file through exactly one
confirmed privileged step that prints the commands it will run verbatim, and a
default Lima networks configuration when none exists. It never edits an
existing networks configuration, and the user-v2 network needs no root
components at all.

A root-owned installation record identifies provider, cluster, installation,
node role, ownership mode, and installed versions. Local repair uses this
record and host state, not an arbitrary active kubeconfig. Joining a multi-node
K3s cluster is initiated independently on each host; the first version does not
store SSH credentials or require a permanent privileged host agent.

Existing-cluster mode (removed 2026-07-27): R4 additionally delivered an
installer mode that installed only the Skali bundle into an externally
managed Kubernetes cluster from an administrator workstation with an
explicit kubeconfig. The mode worked, but it contradicted the mission
(section 2): it made every subsequent feature answer "and what if Skali
does not control the host", splitting registry access, node capabilities,
addresses, and upgrades into managed and unmanaged variants, and it traded
the single tested substrate combination for a support matrix Skali cannot
control. It is removed from the product; Skali supports exactly the hosts
it provisions. Git history retains the implementation, and installing onto
externally managed Kubernetes is a possible later product (section 18),
not a standing constraint on every design.

### 14.2 Default installation topology

V2 initially keeps one Skali installation responsible for one Kubernetes
cluster. K3s is the first distribution provisioned by the installer; the
`skalid` Kubernetes client and service renderers remain distribution-neutral:

```text
Linux hosts
  K3s/Kubernetes
    skali-system                         installer-owned
      skalid Deployment
      bootstrap Skali Postgres (CNPG, skalid state only)
      managed OCI registry
      builder services and blessed operators
    skali-platform                       skalid-owned, explicitly delegated
      shared database/object-storage systems and build jobs
    project/environment namespaces       skalid-owned
      applications, databases, buckets, volumes, and routes
```

- `skalid` runs as a Deployment and accesses the Kubernetes API through a
  scoped in-cluster service account.
- Skali Postgres and the OCI registry are separate stateful workloads by
  default; supported installations may instead provide external services.
- Registry data uses installer-owned durable storage or an independent object
  store. It never depends on an ordinary project bucket.
- Bootstrap images for `skalid`, Postgres, the registry, and required operators
  come from an upstream/bootstrap source or offline bundle; the managed registry
  cannot be required to start itself.
- The public API/UI and registry push endpoints are exposed through explicit
  installer-owned routing and TLS configuration.
- Kubernetes and blessed operators own generic runtime orchestration.

The registry is reachable by cluster nodes and authenticated build clients. A
scoped credential/token flow permits a local CLI or cloud builder to push only
the artifacts assigned to its build. Registry contents are addressed and
verified by digest before revision creation.

### 14.3 Node capabilities, cluster layout, and initialization

Placement policy is expressed through designated node capabilities. Installing
a node assigns its capabilities explicitly, and placement decisions consume
them as policy:

- `application`: runs project application workloads.
- `database`: hosts CNPG database clusters, including the bootstrap database.
- `object-storage`: hosts SeaweedFS volume servers and related components.
- `registry`: eligible to host the managed OCI registry and its storage.
- `edge`: terminates ingress for routes, the API/UI, and the registry
  endpoint.

Capabilities are stamped as node labels at install/join time and recorded in
the root-owned installation record. They are orthogonal to the K3s role: a
host joins as a server or an agent, and the server count (one, or three for a
highly available Kubernetes control plane) is a separate decision from any
capability. A single-node installation carries every capability; a dedicated
fleet assigns them narrowly.

Setup is per-node installation followed by one explicit initialization:

1. Run `skali cluster` on each host, choosing its K3s role and capabilities.
2. After the intended nodes have joined, run `skali cluster init` once on a
   server node.
3. `init` reads the cluster layout from node labels and installation records,
   derives the data-service topology from it, and applies the system bundle in
   dependency order: blessed operators, the bootstrap database sized to the
   derived availability tier, the managed registry, and finally `skalid` wired
   to its generated credentials.

Database availability tiers derive from the number of database-capable nodes:
one node runs a single instance without HA, two nodes run asynchronous
replication, and three or more run synchronous quorum replication. The same
derivation applies to the bootstrap database and to the default shared pool.
Tier changes are explicit maintenance actions, never automatic side effects of
a node joining or leaving. `skalid` may surface that a tier upgrade is
available; the operator applies it deliberately, and each owner scales its
own databases: the installer scales the bootstrap database, while `skalid`
scales its platform pools through an explicit product operation.

The handoff from installer to `skalid` transfers bookkeeping and observation,
never mutation authority. `init` leaves behind the root-owned installation
record and an in-cluster copy. On first boot `skalid` imports it, learns the
layout and the identities of the bootstrap systems, and begins observing them
as platform subsystems.

### 14.4 Bootstrap database and registry ownership

`skalid` state lives in a dedicated CNPG cluster in `skali-system` that serves
no other consumer. The installer renders it with the same shared packages and
availability tiers as `skalid`-owned pools, so behavior stays uniform, but
ownership does not: the installer creates and mutates it, and `skalid` only
observes it. It is modeled as a platform subsystem, not as a substrate
`DatabaseCluster`, so the claim substrate needs no protected special case and
can never place a tenant in it or plan changes against it. The boundary is
mechanical: `skalid`'s service account has read-only access to `skali-system`.
Tier changes, storage growth, version upgrades, and backup configuration for
the bootstrap database are installer operations.

`skalid` state uses PostgreSQL through CNPG. There is no SQLite or embedded
storage mode and no dual-engine abstraction. CNPG ships with every
installation because managed databases are a core product feature, so the
bootstrap database adds one small cluster to an operator that is already
present, while file-backed state on a single volume could not participate in
the database-node availability tiers that protect the rest of the system. The
k3s single-server SQLite precedent points the same way: k3s itself requires a
real datastore for HA.

The managed registry follows the same ownership rule. It is CNCF Distribution
deployed as a single instance pinned to a registry-capable node, with durable
data on an installer-owned volume by default; a supported external S3 endpoint may replace that storage without
changing the model. It is never backed by the in-cluster project object
store, which it may be required to bootstrap. The K3s embedded registry
mirror (Spegel) is enabled so images already pulled anywhere in the cluster
remain available for pod rescheduling while the registry is down. A highly
available registry would require shared blob storage and is deliberately
deferred; it would not change registry ownership. Registry mutations are
installer operations; `skalid` observes health, capacity, and retention and
manages artifact contents through the normal build and import APIs.

### 14.5 Ownership, failure, and recovery

`skali cluster` and `skalid` use disjoint ownership labels, field managers,
RBAC, and prune scopes. The server-side-apply field manager keeps the literal
value `skali-installer` for continuity with existing installations. `skalid`
may reconcile explicitly delegated shared platform services and report
bootstrap health, but it cannot reconcile or
delete the resources required to run itself. Bootstrap upgrades and repairs
are installer operations, not project deployments.

If `skalid` or Skali Postgres is unavailable, already-created Kubernetes
workloads continue running while product mutations pause. If the Kubernetes API
or K3s service is unavailable, the in-cluster API/UI may also be unavailable;
the administrator runs the installer on the affected host for diagnosis and
repair. Cluster-loss recovery comes from reproducible installer inputs plus
off-cluster backups of Skali state and irreplaceable registry artifacts.

The local development installation uses the same in-cluster component boundary
with a disposable profile. A future external management plane may coordinate
multiple clusters, but multi-cluster control and management-plane HA must not
complicate the initial domain model.

## 15. Rewrite strategy

### 15.1 Headless delivery order

The implementation sequence is contract-first and headless:

1. Typed definition, values, plan, revision, installation-ownership, and
   recovery contracts.
2. Headless public API and CLI commands with fixture-backed output.
3. Reusable installer-owned system bundle and disposable local target profile.
4. Build/Artifact/registry pipeline.
5. Observed-state and reconciliation kernel.
6. Complete application deployment through CLI, including local development,
   run steps, status, and logs.
7. Production installer plus remote deployment and cloud-build workflows.
8. Shared database and object-storage subsystems through the same headless
   contracts.
9. Web status surfaces, visual definition editor, and operational hardening.

A temporary developer-only HTML page may aid debugging, but product UI business
logic does not begin until the CLI can complete the application slice. This
keeps definition and API semantics independent of whichever forms and screens
are later chosen.

### 15.2 Git strategy

1. Keep the current `rework` branch and remote branch intact.
2. Create the v2 branch from `c4eb838`.
3. Bring this plan onto that branch first.
4. Retain auth migrations `00001-00003`.
5. Start the new product schema at `00004` or create a clearly named v2
   baseline before any public release.
6. Port current UI primitives and independent security fixes deliberately.
7. Use current tests, golden manifests, API behavior, and screenshots as a
   catalog; rewrite tests against the new contracts.
8. Do not preserve internal APIs merely to reduce diff size.

### 15.3 Migration policy

If no external production installation depends on the current rework schema,
do not build an automatic data migration. The v2 rewrite is a new development
baseline.

If real installations exist before cutover, add an explicit one-time importer
from current projects/environments/services into v2 definitions and values. Do
not make the v2 core permanently understand both schemas.

## 16. Milestones and exit criteria

### R0 - Architecture contract

Status: accepted 2026-07-19. Every deliverable exists in the repository as a
schema, fixture, transcript, or lifecycle contract. One scope note: the alpha
manifest schema deliberately includes fields ahead of the first runtime slice
(bucket lifecycle and versioning, project backups, release commands, and
placement spread). They compile into the canonical definition and claims
today; their runtimes land in later milestones, and plan/deploy must say
clearly when a target does not yet implement a compiled feature.

Deliver:

- Final terminology and invariants.
- Manifest and environment-values alpha schema.
- A checked-in JSON Schema generated from the manifest wire types for editor
  completion; semantic validation remains compiler-owned.
- Complete application-only and application/database/bucket examples plus
  focused valid and invalid compiler fixtures.
- Application fixtures covering both an existing image and a local build
  context.
- Expected plans, revisions, dependency graphs, and destructive diffs for those
  fixtures.
- Canonical IR and Kubernetes-rendering golden fixtures for the first
  application slice.
- Database-claim and run/step state machines.
- Build/Artifact state model, registry namespace/retention model, and the
  registry decision: CNCF Distribution as a single instance on a
  registry-capable node with installer-owned durable storage, offline-verified
  token authentication, and the embedded K3s registry mirror.
- Installation topology plus distinct installer/bootstrap, `skalid` platform,
  and `skalid` project ownership, field-manager, RBAC, and prune contracts.
- Cluster-layout and capability schema with valid and invalid fixtures plus
  the derived availability-tier rules.
- Installer state detection and action model for fresh installation, K3s
  server/agent join with capability assignment, cluster initialization,
  existing Kubernetes, upgrade, diagnosis, repair, restore, and uninstall.
- Local-runtime topology decision and complete CLI transcripts for local build,
  cloud build, env-file upload, remote-value reuse, failure, and detach/reattach.
- Interactive and non-interactive installer transcripts for single-node K3s,
  node join, cluster initialization, an availability-tier upgrade after adding
  a database node, existing Kubernetes, repeat execution, degraded state, and
  destructive confirmation.

Exit criteria:

- Every core feature can be described as definition -> revision -> observation.
- No unresolved dual source of truth.
- SeaweedFS metadata and a user PostgreSQL service demonstrably fit the same
  database-claim model on paper.
- The local workflow uses the same revision and reconciliation contracts.
- Local build, cloud build, and external image import all terminate in the same
  Artifact contract and managed-registry digest.
- Non-interactive env-file behavior is explicit and cannot upload a discovered
  file accidentally.
- The product reconciler cannot apply or prune installer-owned bootstrap
  resources.
- The claim substrate cannot place tenants in or plan changes against the
  bootstrap database.
- The installer can diagnose host/K3s state without a working `skalid` or Skali
  Postgres connection.

No product-controller implementation begins before R0 is accepted.

### R1 - Domain and persistence kernel

Deliver:

- Typed definition parser, normalizer, validator, and canonical hasher.
- Environment-values/env-file validator, secret separation, candidate staging,
  and typed reference resolution.
- Immutable revision builder with pluggable artifact resolvers.
- Build, Artifact, provenance, registry-reference, and revision-retention lease
  models.
- Project/environment/target persistence.
- Run/step/attempt/log persistence and APIs.
- Service-module registry with an application test module.
- Pure dependency-graph and health-evaluation contracts.

Exit criteria:

- YAML and equivalent JSON generate identical canonical hashes.
- Reordering maps does not change a revision.
- Invalid/unknown fields fail before target mutation.
- Secrets cannot appear in revision fixtures or run logs.
- A failed candidate preparation does not promote draft/value versions or move
  the environment target.
- Restart tests preserve target and resumable run identity.

### R2 - Observation and reconciliation kernel

Deliver:

- `ObservedStore` and fake implementation.
- Kubernetes LIST/WATCH implementation and cache-readiness state.
- Ownership indexes and affected-owner queue.
- Generic apply/prune engine with an explicit server-side-apply field-ownership
  contract, including fields controlled by Kubernetes subresources and other
  controllers.
- Safe fixed-to-HPA and HPA-to-fixed transitions. While an HPA is active,
  `skalid` omits and does not own `Deployment.spec.replicas`; transition ordering
  prevents a transient reset to Kubernetes' default replica count.
- Periodic audit/resync.
- Projection invalidation stream.
- Structured health diagnostics.

Exit criteria:

- Pod create/readiness/delete changes propagate without waiting for the audit
  interval.
- Watch disconnect is visible as stale/unknown and recovers cleanly.
- Deleting a managed stateless object out of band causes healing.
- Reapplying an autoscaled application neither conflicts with the HPA nor resets
  its current replica count.
- API topology reads perform no direct Kubernetes request.
- Restart rebuilds the cache before reporting fresh health.

Implementation notes (decided 2026-07-20, R2 landing):

- Cluster credentials come exclusively from `SKALI_KUBECONFIG` (set: must
  load; unset: in-cluster is attempted). The ambient `KUBECONFIG` variable is
  never consulted so a server daemon cannot silently attach to whatever
  cluster a developer shell points at. Without credentials `skalid` runs
  API-only: observation reports unknown and the reconcile workers idle.
- One deployment run spans prepare, promote, apply, verify, and activation:
  `deploy.Execute` leaves the run running after promote and the reconcile
  worker finishes it through deterministic step keys. Drift healing creates a
  run of kind `reconcile` only when material work happened; a pass that
  changes nothing writes no journal rows.
- Past the rollout deadline (`RECONCILE_ROLLOUT_DEADLINE`, default 10m) the
  run fails with diagnostics. R2 kept the target unconditionally; since the
  R3 landing the section 8.4 automatic-fallback policy applies: the target
  returns to the last active revision through a guarded compare-and-swap
  (a newer promotion always wins), and only the first deployment of an
  environment, having nothing to fall back to, keeps its target with
  level-triggered reconciliation continuing toward it.
- Watch-source freshness counts a successful LIST or the first delivered
  non-error watch event as contact. A bare successful Watch call is not
  contact (the rest client masks connection errors as an empty watcher), and
  error events are not contact either (a booting API server answers watches
  with error statuses long before it can serve a fresh view).
- Secrets are rendered and applied but not observed in R2: the one rendered
  Secret per environment is always in the desired set, so Secret pruning is
  moot and no plaintext-bearing objects enter the observed store.
- Namespaces and PersistentVolumeClaims are never pruned; environment
  deletion stays database-only in R2 and the audit reports orphaned managed
  namespaces without touching them.

### R3 - CLI-managed local installation and application slice

Deliver:

- Headless `skali validate`, `skali plan`, and local-target `skali deploy`.
- `skali dev up`, bare `skali dev`, status, logs, stop, and reset workflows.
- CLI-managed disposable k3d cluster using a pinned k3s version.
- Installer-owned local system bundle containing in-cluster `skalid`, Postgres,
  registry, and blessed components.
- In-cluster `skalid` discovery through an explicit loopback route or
  port-forward and local authentication.
- Blessed base components.
- Dotenv discovery/import with declared secret separation.
- Local build executor, external image import, Artifact creation, and registry
  digest verification.
- Application module: build/image, Deployment, Service, routing, health,
  scaling, resources, and logs.
- Detailed deployment run rendered completely in the CLI. No web UI is required
  for this milestone.

Exit criteria:

- From a fresh machine with declared prerequisites, bare `skali dev` in an
  example project reaches a healthy route without manual kubectl commands.
- Both a build-sourced application and an image-sourced application run from
  local managed-registry digests.
- Repeating an unchanged deployment reuses the artifact and does not create a
  meaningless revision.
- Killing one of three application pods changes health to 2/3 promptly, shows
  its reason, heals, and returns to 3/3.
- Restarting the local `skalid` Pod during rollout resumes toward the same
  revision.
- Stopping and restarting the local cluster retains control-plane state and
  reconstructs observation before reporting fresh health.
- `skali dev reset` clearly confirms and removes the complete local
  installation; a subsequent `skali dev` creates a clean installation.

Implementation notes (decided 2026-07-20, R3 landing):

- The deployment API is a multi-request artifact window driven by a
  `deployments` coordination row (`preparing -> promoted | failed |
  cancelled`, one preparing per environment): open re-runs the plan and
  destructive gate server-side, materializes pending artifacts and running
  build records, and journals the opening steps; the client builds,
  imports, and streams progress through a scoped step-write surface
  confined to the run's `artifacts` subtree; completion re-reads the
  recorded per-application decisions (never client-supplied artifact ids),
  creates the revision, and promotes atomically. The journal never drives
  any of it.
- Artifact dedup keys on a client-computed input hash: sha256 of the
  deterministic context tree hash (ignore rules and hard `.env`/VCS
  exclusions applied), the build configuration hash (Dockerfile content,
  target, resolved plain arguments), and the platform. An unchanged input
  reuses the verified artifact; imports reuse by exact upstream reference,
  so a moved upstream tag never changes an existing revision.
- Builds live in a `builds` table that drives them (origin `local` |
  `worker`): local builds are records created already running by the CLI,
  kept alive by heartbeats and swept with their deployment when the client
  goes quiet; the queued/claimed states and lease columns are the dormant
  R4 worker protocol. Build logs live only in the journal.
- Local builds run BuildKit through `docker buildx` with attestations
  disabled so the pushed digest is the plain manifest digest; imports copy
  manifests registry-to-registry (go-containerregistry) preserving digests
  exactly; the server verifies every digest against the managed registry
  before an artifact verifies (the supported feature set is documented in
  docs/build-matrix.md). The local registry is anonymous and
  loopback-only; the token protocol arrives with the production registry
  in R4.
- The local platform (cluster `skali-dev`, HTTP-only edge on 8080,
  registry localhost:5510 via NodePort 30500 and a containerd mirror)
  applies the shared skali-system bundle under the `skali-installer` field
  manager: CNPG operator and `Cluster` for skalid state, CNCF
  Distribution, in-cluster skalid (in-cluster credentials, migrate
  initContainer), and a bootstrap operator-user Job. cert-manager and TLS
  issuance are production-bundle concerns (R4); the rendered
  cluster-issuer annotation is inert locally. The CLI logs in through the
  edge and stores the `local` remote; a `skali-dev` cluster without an
  installation record is never adopted or destroyed.
- Runtime logs stream through the API as a deliberate cluster pass-through
  (kubelet follow per member, previous-container tail after restarts);
  they never enter the system database and are never mixed with
  deployment-step logs.
- Installations declare capabilities (`SKALI_CAPABILITIES`, default
  `application;edge`); deployment open rejects revisions needing more with
  the missing capabilities named, so a databases-using manifest fails fast
  in R3 instead of stalling unhealthy.

### R4 - Production installer, remote artifact, and cloud-build pipeline

Deliver:

- Privileged installer core with interactive state detection and an explicit
  non-interactive configuration format.
- Fresh single-node K3s creation, K3s server/agent join, and Skali installation
  into an explicitly selected existing Kubernetes cluster (existing-cluster
  installation removed 2026-07-27, section 14.1).
- Repeat-run status, versioned upgrade, diagnosis, repair, restore entry point,
  and scoped uninstall operations.
- Root-owned installation identity and strict separation between installer-
  owned bootstrap resources and `skalid`-owned product resources.
- Production managed-registry installation, scoped authentication, and health
  as reported by the installer. Capacity observation moved to R7 (2026-07-23).
- Remote `skali plan` and `skali deploy` through the headless API.
- `--env-file` upload and `--use-remote-env` behavior for remote environments
  (superseded 2026-07-22: `--use-remote-env` was removed, stored remote values
  are the default, and an upload happens only with an explicit `--env-file`,
  section 11.3).
- Local build-and-push to a remote Skali registry.
- Unified deployment run tree across local executor steps, artifact
  verification, revision preparation, and rollout.
- macOS host mode: `skali cluster` manages one headless Linux VM per Mac via
  Lima (bridged networking by default, login LaunchAgent autostart) and
  installs the node inside it; Linux hosts stay native. It provisions its own
  Lima and socket_vmnet dependencies (pinned, checksum-verified, one confirmed
  privileged step).

Deferred out of R4 on 2026-07-23 (see section 18): the managed cloud builder,
external image import/cache with retention leases, and the local/LAN HTTP-only
installation profile.

Exit criteria:

- From a supported fresh Linux host, the installer reaches a healthy
  single-node K3s and in-cluster Skali installation without manual `kubectl`.
- Re-running the installer detects the installation and performs no mutation
  until an explicit maintenance action is selected.
- A second host can join as a K3s agent through an explicit enrollment flow.
- Existing-Kubernetes mode installs Skali without claiming node or Kubernetes-
  version lifecycle (retired 2026-07-27 with the mode's removal).
- Installer diagnostics remain available when `skalid` or Skali Postgres is
  unavailable, and uninstall scopes distinguish Skali, the current node, and
  the whole cluster.
- A build or upload failure leaves remote values, target revision, and
  active revision unchanged.
- Registry credentials are scoped, short-lived, and cannot push outside their
  assigned namespace.
- From a supported macOS host, the installer reaches the same healthy
  single-node state inside a Lima-managed VM without sudo on the Mac, and
  node-scope uninstall removes the VM entirely.

Implementation notes (landed through 2026-07-27):

- The standalone `skali-installer` binary was folded into the CLI as the
  `skali cluster` group on 2026-07-22; the server-side-apply field manager
  keeps the literal value `skali-installer` (section 14.5).
- Landed beyond the original deliverable list: highly available server joins
  (every server runs etcd; one or three servers), availability-tier
  reporting and explicit tier upgrades, diagnosis and repair, scoped
  uninstall, a restore entry point, the `skali remote` group replacing the
  earlier auth/context commands, the per-checkout deploy-target binding
  (section 6.1), build platforms following the server-reported node
  architectures, and explicit network declarations for multi-homed hosts
  (bind address, tls-san entries, and the coordinator address recorded at
  install time).
- Registry access landed as normal `docker login` against the public
  registry domain with short-lived scoped tokens; a stored push-credential
  approach was rejected and parked on a branch. The composite join token
  carries both K3s enrollment and Skali registry trust.
- The plan's pointer-typed installation-record node section was not
  adopted; `Record.Node` stays a value type.
- Existing-cluster mode was implemented per the original deliverable and
  then removed by the 2026-07-27 decision (section 14.1). The removal is
  complete: the mode flags, config schema, detection states, bundle
  External profile, and the skalid-side pull-secret and ingress-class
  knobs that existed only for unmanaged hosts are all deleted.
- Live end-to-end verification of the final R4 tracks (tier, diagnose,
  repair, HA join) is still outstanding; unit tests and vet are green, and
  the Linux and macOS cluster e2e suites pass for install, join, and
  upgrade.

### R5 - Shared database substrate

Deliver:

- PostgreSQL/CNPG engine driver.
- DatabaseClaim, DatabaseCluster, DatabaseTenant, placement, output, and
  observation models.
- Shared, owner/project, and dedicated policies.
- Availability policies and topology observation.
- User database service module.
- Application connection outputs and secret mirroring/injection.
- System database-claim API for internal consumers.
- Backup/restore run skeleton, even if production policy lands during
  hardening.

Exit criteria:

- A user database and a synthetic system database use exactly the same claim,
  placement, cluster, tenant, credential, and observation paths.
- Application waits visibly for database output and then starts.
- CNPG primary/replica changes update observed topology without request-time
  reads.
- Credential values never enter definitions, revisions, activity, or run logs.
- Database deletion requires an explicit destructive plan.

### R6 - Object-storage substrate and service

Deliver:

- SeaweedFS physical-system module and observer.
- Metadata database expressed only as a system DatabaseClaim.
- BucketClaim, allocation, quota, credential, output, and teardown paths.
- User object-storage service module.
- Application bucket bindings.
- Internal and optional external endpoints.
- Platform and bucket health projections.

Exit criteria:

- Fresh bootstrap visibly waits on the ordinary database claim and then brings
  up the filer/gateway.
- There is no object-storage-specific database provisioner.
- A bucket created from the definition is usable by the connected application.
- Quota, credential rotation, failure, and deletion have structured run steps.
- A SeaweedFS observation failure becomes stale/degraded without blocking
  unrelated application observation.

### R7 - Product UI and platform completion

Deliver:

- Project/environment/service/deployment/system web surfaces on v2 APIs.
- TLS, edge routing, node capabilities, and platform settings expressed as
  platform policies/modules.
- Cancel and rollback UX.
- Activity, metrics, and runtime-log surfaces.
- Registry health and capacity observation in the system surfaces (moved from
  R4 on 2026-07-23).

Exit criteria:

- The same example definition deploys locally and remotely with only declared
  environment values/capabilities changing.
- The revision view explains artifact/runtime-version differences.
- Web and CLI display the same target, active revision, health diagnostics, and
  run tree.
- Editing an application in the web UI produces the same definition diff and
  plan as editing `skali.yml`; there is no UI-only service mutation
  path.
- Restarting the in-cluster API/UI does not change target state, and it reports
  fresh observations only after watch synchronization.
- During a cluster-control-plane outage, the installer provides the independent
  diagnosis path; the API/UI does not claim to be an out-of-cluster recovery
  surface.

### R8 - Durability and release hardening

Deliver:

- Tested database backup and restore.
- Off-cluster backup policy for Skali state and system metadata.
- Registry artifact durability/backup policy and tested retention/garbage
  collection.
- Installer, K3s, Kubernetes API, operator, and Skali upgrade/version
  compatibility tests.
- Failure injection and long-running reconciliation tests.
- Retention policies for runs, activity, metrics, and logs.
- Security/redaction audit.
- Documentation and example-project suite.

Exit criteria:

- Restore tests prove backups rather than only creating them.
- Failure matrix covers pod, node, API watch, operator, SeaweedFS, Postgres,
  registry, builder, `skalid`, and network interruptions.
- No test requires events or run logs to restore desired state.
- The public v2 schema and compatibility rules are documented.

## 17. Verification strategy

### 17.1 Definition and revision tests

- Golden parser/normalizer fixtures.
- YAML/JSON semantic-equivalence tests.
- Unknown-field and version tests.
- Typed-value and secret-reference tests.
- Image/build mutual-exclusion, build-context path, and context-hash tests.
- Dotenv parsing, declared-secret separation, missing/unknown key, and redaction
  tests.
- Stable-key rename/removal and destructive-plan tests.
- Deterministic dependency graph and revision checksum tests.

### 17.2 Service module tests

- Pure desired-resource golden tests.
- Health evaluation from observed fixtures.
- Dependency/output tests.
- Removal/data-loss classification.
- No generic-core changes required when registering a test service kind.

### 17.3 Live k3d tests

- Deploy, observe, break, heal, cancel, rollback, and delete.
- Kill pods and nodes; corrupt readiness; remove managed objects.
- Restart `skalid` during artifact preparation and rollout.
- Disconnect/restart the Kubernetes API and watches.
- CNPG failover and unavailable operator.
- SeaweedFS master/filer/gateway failure.
- Stop/restart the local cluster while retaining its persistent state, then
  prove observation and reconciliation recover.
- Destroy the local installation, restore from an exported/off-cluster backup,
  and prove target state can be reconciled when restore support lands.
- Disable an upstream registry after import and prove a retained revision still
  pulls from the managed registry.
- Interrupt local/cloud builds and registry uploads, then verify target/value
  atomicity and safe retry.

### 17.4 CLI tests

- Project-root discovery.
- Missing local-development prerequisite messages.
- First-run local bootstrap.
- Idempotent repeated `skali dev`.
- Local/cloud executor selection, build-cache, changed-source, and external
  image-import behavior.
- Interactive env-file selection and non-interactive explicitness.
- Remote-value reuse without reading or overwriting secret values.
- Terminal interruption and reattachment to a run.
- Safe reset confirmation.

### 17.5 Installer tests

- Fresh-host, existing-installation, joined-node, degraded, and unsupported-host
  state detection.
- Idempotent repeat execution and explicit non-interactive configuration.
- Single-server creation and server/agent enrollment flows.
- Existing-Kubernetes mode never performs host or Kubernetes-version lifecycle
  operations (retired 2026-07-27 with the mode's removal).
- Ordered upgrade planning, interrupted upgrade diagnosis, and scoped repair.
- Separate confirmation and ownership checks for removing Skali, removing the
  current node, and destroying a complete cluster.
- Diagnostics that work while `skalid`, Skali Postgres, or the registry is
  unavailable.

### 17.6 Security tests

- Secret values absent from canonical definition, revisions, logs, activity,
  error messages, metrics, and list APIs.
- Local API loopback/access-token boundaries.
- Remote role and reauthentication gates.
- Credential reveal and rotation audit entries.
- Malicious manifest paths/build contexts cannot escape the project root without
  explicit permission.
- Scoped registry credentials cannot read or write unauthorized project
  namespaces.
- Build secrets do not persist in image history, build cache metadata, or logs.
- Selected env files and common `.env` files do not enter cloud build contexts.

## 18. Scope boundaries

Required for the v2 core:

- Project definition and environment values.
- Headless validate/plan/deploy/status/run/log workflows.
- Local builds through one Build/Artifact contract that cloud builds adopt
  when they land.
- Integrated managed OCI registry for build uploads.
- Immutable revisions, target/active pointers, plan, deploy, cancel, rollback.
- Observed-state store and responsive health.
- Execution runs with detailed steps and logs.
- Application, PostgreSQL database, and object-storage services.
- Shared database substrate for user and system claims.
- CLI-managed local development runtime.
- Privileged interactive/non-interactive installer for K3s and Skali bootstrap,
  node join, upgrade, diagnosis, repair, restore, and scoped uninstall.
- Live application logs and basic metrics.
- Remote deployment through the same model.

Important but layered after the core proves itself:

- Environment priority and advanced placement policies.
- Database relocation/capacity spill.
- Erasure coding and object lifecycle policies.
- The managed cloud builder, external image import/cache, retention leases,
  and the cloud build-context upload protocol, deferred from R4 on 2026-07-23:
  they land as one slice with git-triggered push-to-deploy (native GitHub
  integration), since cloud builds are only needed once pushes deploy without
  a local CLI. Buildpacks and horizontally scalable remote builder pools layer
  after that slice.
- Local/LAN mode (an explicit HTTP-only installation profile for non-public
  domains: init without a TLS issuer email, no cert-manager or ACME issuer,
  api/ui and registry edges without TLS blocks), deferred from R4 on
  2026-07-23 and rescheduled into a later milestone when prioritized.
- Installation onto an externally managed Kubernetes cluster (the
  existing-cluster mode removed on 2026-07-27, section 14.1): it may return
  as a deliberately scoped product once the owned-host platform is complete,
  but until then no feature is designed around unmanaged hosts.
- Templates/marketplace and Compose import.
- More database engines.
- Multiple application processes, sidecars, and cron jobs.
- Management-plane HA and multi-cluster control planes.
- Multi-edge traffic distribution.
- Per-project membership and CI tokens.

These later features must be expressible as new service-module behavior,
infrastructure policy, operation type, or client workflow. None should require a
new authoritative state plane.

## 19. Decisions fixed before implementation

The following decisions are part of this plan:

- Restart the product core from `c4eb838`; do not start an empty repository.
- Keep the current `rework` branch as reference.
- `skali.yml`/`skali.yaml` or an explicitly selected alternative is the
  portable project definition.
- YAML/JSON are syntax over one typed, versioned semantic model.
- The headless API and CLI form a complete product before the visual UI is
  rebuilt.
- The web UI edits and observes the same definition/plan/deploy contracts; it
  has no parallel service-management semantics.
- Environment values are separate and typed; dotenv files are a CLI import
  format, secrecy is declared by the manifest, and there are no arbitrary
  overlay patches.
- Non-interactive deploys use the remote stored values unless an explicit
  `--env-file` is passed; Skali never guesses which secrets to upload.
- Application sources support either an existing OCI image or a project-relative
  build definition.
- Build executor selection (`local`, `cloud`, or policy-driven `auto`) is a
  deployment choice and does not change application semantics.
- Every build/import produces a verified Artifact in the target installation's
  managed OCI registry before revision creation.
- The managed registry is also the installation-local cache for external
  images, and retained revisions lease their artifacts against garbage
  collection.
- Deployment produces an immutable, resolved revision.
- Environments have explicit target and last-active revision pointers.
- Cluster knowledge comes from a fresh/stale-aware observed-state store fed by
  watches, with resync as a backstop.
- Generic reconciliation does not switch on application/database/bucket fields.
- User and system databases share one lower-level database claim subsystem.
- Object storage depends on that subsystem for metadata rather than provisioning
  a special pool.
- Runs/steps/logs explain reconciliation but never drive it.
- Local development is a distinct local Skali installation using the same core.
- The developer-workflow commands own project files, builds, terminal
  workflows, named Skali remotes, and disposable local development; they are
  not an API mirror and do not administer production Kubernetes.
- The privileged `skali cluster` group owns host-level K3s and installer-owned
  Skali system lifecycle, remains independent of the Skali API/database, and
  supports both interactive and explicit non-interactive operation.
- `skalid`, Skali Postgres, and the managed registry run inside Kubernetes by
  default as separately scalable workloads; supported external Postgres and
  registry providers do not change the reconciliation model.
- Installer-owned bootstrap resources and `skalid`-owned platform/project
  resources have disjoint ownership and prune boundaries.
- Node capabilities (`application`, `database`, `object-storage`, `registry`,
  `edge`) are assigned at node installation, stamped as labels, and recorded;
  `skali cluster init` applies the system bundle from that recorded layout.
- Database availability tiers derive from database-capable node count: one
  node is single-instance, two asynchronous, three or more synchronous. Tier
  changes are explicit installer operations, never side effects of node
  membership changes.
- `skalid` state is PostgreSQL on a dedicated, installer-owned CNPG bootstrap
  cluster; there is no SQLite mode and no dual-engine abstraction.
- The bootstrap database is observed as a platform subsystem; it is never a
  substrate `DatabaseCluster` and never accepts claims.
- System database claims, including object-storage metadata, place onto
  `skalid`-owned platform pools, never the bootstrap cluster.
- The managed registry runs as a single instance on registry-capable nodes
  with installer-owned durable storage; the K3s embedded registry mirror keeps
  already-pulled images available during registry downtime.
- The installer handoff transfers observation and bookkeeping to `skalid`;
  mutation authority over bootstrap resources stays with the installer.
- The managed registry implementation is CNCF Distribution with registry
  token authentication: the installer provisions the signing keypair,
  `skalid` mints scoped short-lived push tokens and refreshed pull tokens,
  and token verification never calls `skalid`.
- Release artifacts live under `skali/<project>/<application>` and imported
  content under `cache/<host>/<path>`; transparent registry mirroring stays
  disabled so all external content enters through verified imports.

Added 2026-07-27:

- Skali is an open-source, self-hostable alternative to hosted application
  platforms and self-hosted deployment tools; Kubernetes is its execution
  substrate, never its user-facing interface (section 2).
- Skali is both the cluster manager and the application platform, and it
  owns its hosts: the only supported substrate is the pinned k3s and bundle
  combination the installer provisions, on native Linux or inside the
  managed Lima VM on macOS.
- Existing-cluster mode is removed. Features are designed for
  Skali-managed hosts only, and installing onto externally managed
  Kubernetes is a possible later product, not a standing design constraint.

## 20. R0 questions and their resolutions

Every question this section originally tracked is resolved or explicitly
re-homed to the milestone that implements it:

- Manifest field names and typed value-reference syntax: resolved by the
  manifest schema, the compiler fixtures, and the example projects.
- Web editor form: the managed source mode edits the semantic document only;
  YAML comments survive only in `file` mode. R7 implements it.
- Local prerequisite: the first release requires Docker. A Lima provider is a
  later local-runtime provider, not a v2 blocker.
- Local builder: BuildKit through the Docker daemon. The cloud build-context
  upload protocol and provenance metadata are fixed alongside the builder
  service, which moved to the deferred push-to-deploy slice (section 18).
- Installer state file and layout: resolved by the cluster-layout schema and
  the installer transcripts. Each installer release pins one k3s version;
  multi-node upgrade sequencing UX lands in R4.
- Registry: resolved; see the registry decision in section 10.6.
- Step-log streaming uses SSE, matching the existing web transport. The
  run-log retention caps were fixed with the R1 journal persistence
  (constants in `internal/journal`): 2000 entries per attempt with the final
  slot holding a single truncation marker, 8 KiB per message with a
  truncation suffix, the newest 50 terminal runs kept per environment, and
  terminal runs deleted after 90 days. Promotion to configuration is
  deferred until a deployment needs different limits.
- Backup destination: an external S3 endpoint configured at initialization;
  wiring and restore tests land in R8.
- Build schema: resolved at the alpha level (context, dockerfile, target,
  arguments). The supported BuildKit feature matrix is documented in R3.
