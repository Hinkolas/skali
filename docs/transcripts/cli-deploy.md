# Remote plan and deploy transcripts

Context: the developer has an authenticated remote `skali.example.com` for
the installation at `https://skali.example.com/api` (added once with
`skali remote add`, see `cli-remote.md`), and the project checkout
contains `skali.yml` plus a gitignored `.env.production`.

The first successful plan or deploy links the checkout to its target in
`.skali/target.yaml` (remote master URL, project, default environment) and
makes the directory self-ignoring. The transcripts show the link note where
it appears; once linked, the bound target applies without the note.

## 1. Plan with a local env file

```console
$ skali plan --environment production --env-file ./.env.production
remote       skali.example.com (https://skali.example.com/api)
project      file-sharing (skali.yml)
environment  production
linked to remote skali.example.com, project file-sharing, environment production; stored in .skali/

.env.production: 1 plain, 1 secret value (values are validated, not shown)

plan against active revision 2a91a76b
  update  applications.web   artifact 9f2c41d8b1c7 replaces 11ba90c2f4e9
  value   SESSION_SECRET     update (secret)

no destructive changes
```

Planning validates the candidate definition and values server-side and never
mutates the environment: no values are stored, no target moves. Writing the
checkout binding is local tool state, not an installation mutation, so plan
links too.

## 2. Interactive deploy: environment selection and env-file override

The environment's stored values are the default. In a checkout not yet
linked, bare `skali deploy` asks for the environment when several exist,
then offers the project root's `.env` and `.env.*` files as an explicit
override:

```console
$ skali deploy --build=local
remote       skali.example.com (https://skali.example.com/api)
project      file-sharing (skali.yml)
◆ Which environment should Skali use?
└ production
environment  production
linked to remote skali.example.com, project file-sharing, environment production; stored in .skali/

◆ Override production with a local env file?
└ .env.production
values       .env.production (1 plain, 1 secret)

plan against active revision 2a91a76b
  update  applications.web   a new artifact replaces 11ba90c2f4e9
                             artifact will be rebuilt
  value   SESSION_SECRET     update (secret)

◆ Continue with this deployment?
└ Yes

run 01J9V2E8  deploy file-sharing to production
  ok  Validate project definition
  ok  Prepare environment values
        .env.production: 1 plain, 1 secret staged
  ok  Prepare artifacts
        ok  web
              ok  Build locally (BuildKit)                            38s
              ok  Push cr.skali.example.com/skali/file-sharing/web
              ok  Verify sha256:9f2c41d8...
  ok  Create revision 8d1e15b3
  ok  Prepare environment
  ok  Provision dependencies
        ok  databases.data
              ok  Select database cluster: shared-1
              ok  Create logical database and role
              ok  Publish connection outputs
        ok  buckets.files
              ok  Ensure bucket
              ok  Publish S3 connection outputs
  ok  Apply applications
        ok  web
              ok  Apply Deployment, Service, and Ingress
              ok  Wait for rollout: 2/2 ready                         21s
  ok  Verify revision health
  ok  Activate revision

active revision 8d1e15b3 (previously 2a91a76b)
  https://files.example.com  ->  applications.web
```

While a choice is active, the same block shows its keyboard hint and choices;
it settles to the compact `◆`/`└` form above before deployment rendering
starts:

```console
◆ Which environment should Skali use?  (use arrow keys, enter to select)
│ ● production
│ ○ staging
```

Notes pinned by this transcript:

- The environment's stored values are the default value source. Interactive
  use offers the discovered `.env` and `.env.*` files as an override
  selection naming the file, project, environment, and value counts, never
  the values; declining keeps the stored values. Non-interactive use uploads
  only with an explicit `--env-file` and must name the environment, either
  with `--environment` or through the checkout binding.
- Once linked, the bound environment is the default: bare `skali deploy`
  targets it without asking, and `--environment` overrides it for one
  invocation without rewriting the binding.
- The binding's master URL selects the remote, resolved among this machine's
  remotes by URL rather than name; it wins over the machine's current
  remote. Without a matching remote the command fails and points at
  `skali remote add`.
- Staged values are promoted atomically with the target change, after
  artifacts verify.
- The push to `cr.skali.example.com` authenticates with the remote's
  session token automatically: builds export the image locally and skali
  uploads it in-process, the same way imports copy upstream images, so
  docker never contacts the managed registry and nothing is written to the
  docker config or keychain; no docker login is needed. Grants are scoped
  server-side: a session may push only project release repositories and the
  import cache, never arbitrary ones.
- The CLI streams the run tree, but the run is server-owned: every step
  survives the terminal.

## 3. First deploy against a fresh installation: creation and binding

When the project does not exist on the installation yet, interactive
`skali deploy` offers to create it, prompts for the first environment name,
and links the checkout:

```console
$ skali deploy --build=local
remote       skali.example.com (https://skali.example.com/api)
project      file-sharing (skali.yml)
◆ Create project file-sharing on skali.example.com?
└ Yes
◆ Environment name
└ production
◆ Create environment production in project file-sharing?
└ Yes
environment  production
linked to remote skali.example.com, project file-sharing, environment production; stored in .skali/

plan for the initial deployment
...
```

Creation is deploy-only and interactive-only. `skali plan` never mutates the
installation, and non-interactive deploys never create:

```console
$ skali plan --environment production
error: project file-sharing does not exist on https://skali.example.com/api; skali plan never changes the installation, run skali deploy to create it
$ skali deploy --environment production --yes
error: project file-sharing does not exist on https://skali.example.com/api; run skali deploy interactively to create it
$ echo $?
1
```

The same policy applies to a missing environment in an existing project:
interactive deploy confirms `Create environment staging in project
file-sharing?` with No selected by default; plan and non-interactive deploy
refuse with the matching guidance.

## 4. The same deploy with a cloud build

```console
$ skali deploy --environment production --build=cloud
...
  ok  Prepare artifacts
        ok  web
              ok  Upload build context (12.4 MB, 143 files; .env and
                  ignored paths excluded)
              ok  Cloud build on builder-1                            44s
              ok  Verify sha256:9f2c41d8...
...
```

The executor changes; the manifest, the resulting artifact contract, the
revision shape, and reconciliation do not. Selected env files and `.env`
files never enter the uploaded build context.

## 5. Non-interactive use

Non-interactive runs must name the environment, either with `--environment`
or through the checkout binding, and approve the plan explicitly; the stored
values apply unless `--env-file` is passed, and nothing is discovered or
uploaded by guesswork:

```console
$ skali deploy --environment production --build=auto --yes
```

In a linked checkout the bound environment applies, so CI needs only:

```console
$ skali deploy --yes
```

`--build=auto` follows installation policy (cloud builder when configured,
local otherwise). A destructive plan is refused even with `--yes`:

```console
$ skali deploy --environment production --yes
error: plan is destructive; review it and re-run with --allow-destructive
$ echo $?
1
```

## 5.1 Force and rebuild

An unchanged project answers `nothing to deploy`. `--force` deploys anyway:
the unchanged revision is re-promoted and every application workload is
restarted with fresh pods (the promotion stamps a restart the reconciler
renders as a pod-template annotation, like kubectl rollout restart).
Databases, buckets, and volumes are never touched: force recreates pods,
never data.

```console
$ skali deploy --yes --force

nothing changed; deploying anyway (--force restarts the application workloads)

run 01J9V3AB  deploy hello-world to production
  ok    Validate project definition
  ...
```

`--rebuild` (implies `--force`) additionally ignores artifact reuse: builds
run again with `--pull --no-cache` and image sources re-import, so a moved
base tag or upstream image is picked up even though the build inputs hash
the same. `skali plan --rebuild` previews the resulting actions. The same
flags exist on `skali dev` for the local platform.

## 6. Failure leaves the environment untouched

```console
$ skali deploy --environment production --build=local
...
run 01J9V3AA  deploy file-sharing to production
  ok    Validate project definition
  ok    Prepare environment values
  fail  Prepare artifacts
          fail  web
                  fail  Build locally (attempt 1)
                          #12 ERROR: process "/bin/sh -c npm run build"
                          exited with code 1

run 01J9V3AA failed: build for applications.web failed

The environment is unchanged:
  target revision  2a91a76b (unchanged)
  active revision  2a91a76b (unchanged)
  staged values discarded; stored values unchanged
$ echo $?
1
```

Inspecting it later:

```console
$ skali runs --environment production
RUN       KIND    STATUS     STARTED     REVISION
01J9V3AA  deploy  failed     2m ago      (not created)
01J9V2E8  deploy  succeeded  38m ago     8d1e15b3

$ skali run show 01J9V3AA
run 01J9V3AA  deploy file-sharing to production  failed
  ...
  fail  Build locally (attempt 1)  logs: skali run logs 01J9V3AA --step build.web
```

## 7. Detach and reattach

```console
$ skali deploy --environment production --build=local
...
  run  Apply applications
         run  web
                run  Wait for rollout: 1/2 ready
^C
detached from run 01J9V4QF; the deployment continues on the server
  reattach  skali run attach 01J9V4QF
  status    skali status --environment production

$ skali run attach 01J9V4QF
run 01J9V4QF  deploy file-sharing to production
  ...
  ok  Activate revision
```

Detaching never cancels. Cancellation is explicit
(`skali run cancel 01J9V4QF`) and returns the target to the prior active
revision if the new one has not activated.

## 8. Destructive change confirmation

After renaming the database key `data` to `main` in `skali.yml`:

```console
$ skali deploy --environment production
plan against active revision 8d1e15b3
  remove  databases.data   DESTRUCTIVE: deletes the logical database and its data
  create  databases.main
  update  applications.web  configuration changed

◆ This plan is destructive
└ confirmed
...
```

While active, the description says `Type "production" exactly to continue`;
the entered name is replaced by `confirmed` in the settled transcript.
Interactive confirmation requires typing the environment name.
Non-interactive use requires `--allow-destructive`. Stable keys are the
identity: a rename is a destroy plus a create, and the plan says so before
anything happens.
