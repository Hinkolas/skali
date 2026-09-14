---
name: skali
description: Author and edit skali.yaml manifests and design applications for the skali platform. Use when writing or reviewing a skali.yaml or skali.yml file, building or porting an app to run on skali (stateless containers, managed Postgres, S3 buckets), choosing databases, buckets, values, secrets, or a rollout strategy, or fixing skali validate, compile, plan, or deploy errors.
---

<!-- Managed by "skali skill install"; local edits are overwritten on reinstall. -->

# skali

skali runs container applications with managed PostgreSQL databases and
S3 buckets, all declared in one `skali.yaml` at the project root. The
platform owns its hosts: the same manifest deploys to a local `skali dev`
cluster and to production. Applications run as replicated, disposable
containers; databases and buckets are provisioned from the manifest and
their credentials are injected as environment variables.

## Get the references from the CLI, not from memory

The manifest grammar and the CLI surface belong to a skali release, and
the project's target cluster decides which release that is. The CLI
serves both references at that release, so read them from it and never
guess fields:

- `skali skill read manifest` prints every field, default, unit, and
  validation rule. Read it before writing any part of a `skali.yaml`.
- `skali skill read cli` prints the commands used from the terminal.
- Run both from the project directory: the command follows the project's
  target (the checkout binding, else the current remote) and answers with
  that cluster's release.

`skali validate` is the authority. Run it before finishing. Its errors
name the file, line, column, and path, and a field a release removed or
changed is reported with what replaced it and how to fix it. When
validate says the manifest was reviewed against an older release,
`skali skill read manifest --since <that release>` lists what changed.

Read `architecture.md` before designing or porting an application.

## Invariants that do not move between releases

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
- Removing a database or bucket from the manifest destroys its data;
  deploys ask for typed confirmation.

## Files in this skill

| File | Read it when |
| --- | --- |
| `architecture.md` | Designing or porting an application: state, replication, config, lifecycle, scaling |

Everything version-bound comes from `skali skill read <topic>`.
