# Remote plan and deploy transcripts

Context: the developer has an authenticated remote `skali.example.com` for
the installation at `https://skali.example.com` (added once with
`skali remote add`, see `cli-remote.md`), and the project checkout
contains `skali.yml` plus a gitignored `.env.production`.

## 1. Plan with a local env file

```console
$ skali plan --environment production --env-file ./.env.production
remote       skali.example.com (https://skali.example.com)
project      file-sharing (skali.yml)
environment  production

.env.production: 1 plain, 1 secret value (values are validated, not shown)

plan against active revision 2a91a76b
  update  applications.web   artifact sha256:9f2c41d8... replaces sha256:11ba90c2...
  value   SESSION_SECRET     update (secret)

no destructive changes
```

Planning validates the candidate definition and values server-side and never
mutates the environment: no values are stored, no target moves.

## 2. Interactive deploy: environment selection and env-file override

The environment's stored values are the default. Bare `skali deploy` asks for
the environment when several exist, then offers the project root's `.env` and
`.env.*` files as an explicit override:

```console
$ skali deploy --build=local
remote       skali.example.com (https://skali.example.com)
project      file-sharing (skali.yml)
Environment:
  1) production
  2) staging
Select [1-2]: 1
environment  production

Override the stored values of environment production with a local env file?
  0) no, use the stored values
  1) .env.production
Select [0-1] (0): 1
values       .env.production (1 plain, 1 secret)

plan against active revision 2a91a76b
  update  applications.web   artifact will be rebuilt from ./web
  value   SESSION_SECRET     update (secret)

Continue? [y/N] y

run 01J9V2E8  deploy file-sharing to production
  ok  Validate project definition
  ok  Prepare environment values
        .env.production: 1 plain, 1 secret staged
  ok  Prepare artifacts
        ok  web
              ok  Build locally (BuildKit)                            38s
              ok  Push registry.example.com/skali/file-sharing/web
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

Notes pinned by this transcript:

- The environment's stored values are the default value source. Interactive
  use offers the discovered `.env` and `.env.*` files as an override
  selection naming the file, project, environment, and value counts, never
  the values; declining keeps the stored values. Non-interactive use uploads
  only with an explicit `--env-file` and must name the environment.
- Staged values are promoted atomically with the target change, after
  artifacts verify.
- The push to `registry.example.com` uses the ambient docker credentials;
  log in once with `skali remote token | docker login registry.example.com
  -u you@example.com --password-stdin`. Grants are scoped server-side: a
  session may push only project release repositories and the import cache,
  never arbitrary ones.
- The CLI streams the run tree, but the run is server-owned: every step
  survives the terminal.

## 3. The same deploy with a cloud build

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

## 4. Non-interactive use

Non-interactive runs must name the environment and approve the plan
explicitly; the stored values apply unless `--env-file` is passed, and
nothing is discovered or uploaded by guesswork:

```console
$ skali deploy --environment production --build=auto --yes
```

`--build=auto` follows installation policy (cloud builder when configured,
local otherwise). A destructive plan is refused even with `--yes`:

```console
$ skali deploy --environment production --yes
error: plan is destructive; review it and re-run with --allow-destructive
$ echo $?
1
```

## 5. Failure leaves the environment untouched

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

## 6. Detach and reattach

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

## 7. Destructive change confirmation

After renaming the database key `data` to `main` in `skali.yml`:

```console
$ skali deploy --environment production
plan against active revision 8d1e15b3
  remove  databases.data   DESTRUCTIVE: deletes the logical database and its data
  create  databases.main
  update  applications.web  configuration changed

This plan is destructive. Type the environment name to continue: production
...
```

Interactive confirmation requires typing the environment name.
Non-interactive use requires `--allow-destructive`. Stable keys are the
identity: a rename is a destroy plus a create, and the plan says so before
anything happens.
