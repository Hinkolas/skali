# Local development transcripts

Context: a developer machine with Docker running, inside
`examples/file-sharing`, which contains `skali.yml` and a local `.env`. The
local installation is a distinct, disposable Skali installation owned by the
CLI; nothing here touches a remote environment.

## 1. First run on a fresh machine

```console
$ skali dev
project  file-sharing (skali.yml)
values   ./.env (1 plain, 1 secret; local values never leave this machine)

Local platform is not running. Creating it now.
  ok  Check prerequisites: docker 27.4
  ok  Create k3d cluster skali-dev (k3s v1.33.3+k3s1, pinned)
  ok  Install system bundle (local profile)
        ok  Blessed operators: CNPG, Traefik
        ok  Bootstrap database (tier: single)
        ok  Managed registry (localhost:5510 for pushes)
        ok  skalid (http://skali.localhost:8080)

run 01J9W1KD  deploy file-sharing to local
  ok  Validate project definition
  ok  Prepare environment values
  ok  Prepare artifacts
        ok  web
              ok  Build locally (linux/arm64)                        41s
              ok  Push localhost:5510/skali/file-sharing/web
              ok  Verify sha256:5b77e0a1...
  ok  Create revision 4c19f2aa
  ok  Provision dependencies
        ok  databases.data   (tier single on the local cluster)
        ok  buckets.files
  ok  Apply applications
        ok  web: 2/2 ready
  ok  Activate revision

ready
  http://files.localhost:8080   ->  applications.web
  dashboard                     http://skali.localhost:8080

following logs; Ctrl-C pauses the project (skali dev -d keeps it running)
  web-6d9f7b-1  listening on :8080
```

Pinned by this transcript:

- Bare `skali dev` is the complete paved path: platform, build, deploy,
  attach. No kubectl, no manual steps.
- `./.env` is used automatically when present and announced; `--env-file`
  overrides it.
- The local edge publishes HTTP on host port 8080; routes use `*.localhost`
  names. The local platform is HTTP-only: TLS issuance is a production
  concern, and unsupported production guarantees (TLS, real failover,
  multi-node placement) are simply absent, not simulated.

## 2. Repeat run without changes

```console
$ skali dev
project  file-sharing (skali.yml)
values   ./.env (1 plain, 1 secret)

local platform is running (cluster skali-dev)
artifact for web is current (context unchanged, sha256:5b77e0a1...)
definition, values, and artifacts match active revision 4c19f2aa

nothing to deploy; attaching
  http://files.localhost:8080   ->  applications.web
```

An unchanged deploy reuses the artifact and creates no meaningless revision.
A source change rebuilds only the affected image and prepares a new local
revision.

## 3. Repeat run while a rollout is in flight

```console
$ skali dev
local platform is running (cluster skali-dev)

a deployment is already in flight; attaching to run 01J9W2R4
  ok  Apply applications
        ok  web: 2/2 ready
  ok  Activate revision

ready
  dashboard  http://skali.localhost:8080

following logs; Ctrl-C pauses the project (skali dev -d keeps it running)
  web-6d9f7b-1  listening on :8080
```

Pinned by this transcript:

- Bare `skali dev` never fails with `deployment_in_flight`: a run already
  holding the environment (a rollout still settling, a pause finishing) is
  adopted and attached to instead.
- The attach path skips the env-file prompt and the plan entirely; the
  in-flight run already decided what is being deployed.
- `skali dev --force` (and `--rebuild`) cancels the in-flight run and
  deploys fresh instead of attaching.
- `skali deploy` keeps its explicit `deployment_in_flight` error; adopting
  a running rollout is a dev-only convenience.

## 4. Status, logs, stop, restart

```console
$ skali dev status
platform   running (cluster skali-dev, k3s v1.33.3+k3s1)
project    file-sharing, active revision 4c19f2aa
  applications.web   healthy   2/2 ready
  databases.data     healthy   single instance
  buckets.files      healthy   quota 3% used

$ skali dev logs web
web-6d9f7b-1  GET /health/ready 200
web-6d9f7b-2  GET / 200
^C

$ skali dev stop
stopped local platform; state is retained
$ skali dev start
...
  ok  Start k3d cluster skali-dev (state retained)
  ok  Import skalid:dev (unchanged since last import)
  ok  Converge platform (unchanged since last converge)
local platform running; state is retained
```

Stopping retains volumes and control-plane state; starting hits the fast
path (no bundle converge) once skalid answers through the edge again. After
a restart, health is reported only once observation has resynchronized.
Ending an attached `skali dev` session (Ctrl-C, closing the terminal)
pauses the project like `skali dev down`; `skali dev -d` leaves it running
in the background instead.

## 5. Reset is explicit and total

```console
$ skali dev reset
This destroys the complete local installation:
  cluster skali-dev, its volumes, the local registry and its artifacts,
  local Skali state, and all locally deployed project data.
Nothing outside this machine is affected.

◆ Destroy the local installation?
└ Yes
  ok    Delete cluster skali-dev and volumes
  ok    Remove local registry storage
  ok    Remove local installation record

$ skali dev
Local platform is not running. Creating it now.
...
```

Reset is the only destructive local command, it always confirms (No is the
default; `--yes` skips the prompt for scripts), and the next `skali dev`
builds a clean installation from scratch.
