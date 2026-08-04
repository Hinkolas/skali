---
name: skali
description: Author and edit skali.yaml manifests and design applications for the skali platform. Use when writing or reviewing a skali.yaml or skali.yml file, building or porting an app to run on skali (stateless containers, managed Postgres, S3 buckets), choosing databases, buckets, values, or secrets, or fixing skali validate, compile, plan, or deploy errors.
---

<!-- Managed by "skali skill install"; local edits are overwritten on reinstall. -->

# skali

skali runs container applications with managed PostgreSQL databases and
S3 buckets, all declared in one `skali.yaml` at the project root. The
platform owns its hosts: the same manifest deploys to a local `skali dev`
cluster and to production. Applications run as replicated, disposable
containers; databases and buckets are provisioned from the manifest and
their credentials are injected as environment variables.

Read `architecture.md` before designing or porting an application, and
`manifest.md` when writing any part of a `skali.yaml`. Do not guess
manifest fields: parsing is strict and this reference is complete.

## Rules that will bite you

- Unknown manifest fields are rejected; there are no aliases.
- Every application declares exactly one of `image` or `build`.
- The container filesystem is scratch space and disappears on every
  replace; durable state goes to `databases:` and `buckets:`, never to
  disk. Volumes force a single replica plus recreate rollouts and are a
  last resort.
- Database and bucket credentials come from typed outputs such as
  `{{databases.data.url}}` or `{{buckets.files.access_key}}`; never
  hand-write connection strings or credentials.
- Every `${NAME}` project value is secret: stored encrypted per
  environment, write-only, and shown by name only. There is no `values:`
  declaration block; the contract derives from the references.
- `${NAME}` works in any free-form string field with concatenation, such
  as `"postgres://app:${DB_PASSWORD}@db:5432/app"`. A `{{...}}` service
  output may appear only in `environment:` and must be the whole value.
- Health probes gate rollouts: without a readiness probe the platform
  has a weaker signal that a deployment succeeded.
- Schema migrations belong in `deployment.releaseCommand`, which runs
  once before replicas roll forward.
- Removing a database or bucket from the manifest destroys its data;
  deploys ask for typed confirmation.

## A minimal manifest

```yaml manifest
version: "1"
name: hello-world

applications:
  web:
    build:
      context: .
    ports:
      http:
        port: 8080
    routes:
      public:
        domain: "${APP_DOMAIN}"
        port: http
    health:
      readiness:
        http:
          port: http
          path: /healthz
      liveness:
        http:
          port: http
          path: /healthz
    scaling:
      replicas:
        min: 2
```

`${APP_DOMAIN}` is a per-environment value supplied at deploy time, so
the manifest carries no environment-specific data.

## Reference files

| File | Read it when |
| --- | --- |
| `architecture.md` | Designing or porting an application: state, replication, config, lifecycle, scaling |
| `manifest.md` | Writing `skali.yaml`: every field, default, unit, and validation rule |
| `cli.md` | Interacting with skali from the terminal |
