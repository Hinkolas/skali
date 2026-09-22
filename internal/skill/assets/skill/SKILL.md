---
name: skali
description: Author and edit skali.yaml manifests and design applications for the skali platform. Use when writing or reviewing a skali.yaml or skali.yml file, building or porting an app to run on skali (stateless containers, managed Postgres, S3 buckets), choosing databases, buckets, values, secrets, or a rollout strategy, or fixing skali validate, compile, plan, or deploy errors.
---

<!-- Managed by "skali skill install"; local edits are overwritten on reinstall. -->

# skali

skali runs container applications with managed PostgreSQL and S3 storage,
configured in a project manifest. Use the CLI to establish the release and
read its exact grammar and platform behavior before designing or editing.

## Establish the target and read its references

Run these commands from the project directory, using the same `--remote`
override on every command when selecting a target explicitly:

- `skali skill read architecture` before designing or porting an application.
- `skali skill read manifest` before editing any part of a manifest.
- `skali skill read cli` before using skali commands.

The reference header names the answering release, target, selection source,
and whether its version was verified online or selected offline. The
checkout binding takes precedence over the current remote. With no target,
the installed CLI answers. `--manifest PATH` selects another checkout.
Never guess manifest fields or infer the answering release from `skali version`.

Carry the target and release through authoring and validation. If either
changes, reread the references before continuing. A previously read reference
does not establish the version for the rest of an agent session.

## Validate against the same target

Run `skali validate` before finishing, with the same target selectors.
Errors identify affected fields and required review or migration. Use
`skali skill read manifest --since <revision>` to inspect grammar changes.
Review the selected compiler's behavior before using
`skali manifest upgrade --acknowledge`. History is disposable local `.skali/`
state; never add a version or revision field to a manifest.

`--offline` deliberately uses the target's recorded release and an available
matching CLI. It does not verify the cluster's current release. Use it only
when offline work is intended; reconnect and validate online before deploying.
Failure to obtain a matching CLI is an error, never permission to substitute
another release's documentation.
