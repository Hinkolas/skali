---
name: skali
description: Author Skali manifests, build and deploy applications, and operate Skali through its CLI. Use in any project with a skali.yaml or skali.yml, including whenever it is run, tested, or debugged locally or needs a local database, object storage, or dev environment (use skali dev, never a hand-built setup). Also use for environments and values, routes, managed PostgreSQL and S3, backups, access, cluster installation or maintenance, and troubleshooting Skali commands.
---

<!-- Managed by "skali skill install"; local edits are overwritten on reinstall. -->

# Skali

Skali runs container applications with managed PostgreSQL and S3 storage,
configured in a project manifest. Use the CLI to establish the release and
read its exact grammar and platform behavior before designing or editing.

## Develop locally with skali dev

In a project with a Skali manifest, `skali dev` is the development
environment. It runs the manifest's databases, buckets, routes, and
applications on a local platform with the same resolved environment a
deployment receives. Use it by default: the user does not have to ask, and it
is not an option to offer. Use something else only when the user explicitly
opts out for the task at hand.

- Do not build a parallel setup. No `docker run` or compose services for
  PostgreSQL, S3-compatible storage, or a proxy; no locally installed database;
  no hand-written env files holding connection strings. Applications read the
  variables the manifest provides.
- When the project lacks something for local work (a database, a bucket, a
  migration or seed command, a dev command for an application), add it to the
  manifest and rerun `skali dev` rather than working around it.
- Read `skali skill read cli workflows development` before the first dev
  command. It covers running manifest commands or one-off commands (tests,
  migrations, seeds) on this machine with an application's local environment,
  SQL against local databases, shells in containers, logs, and the platform
  lifecycle.
- `skali dev` stays in the foreground following logs. Check `skali dev status`
  first, since the user may already have a session running. Otherwise start it
  as a background process whose output you can read and stop.
- If `skali dev` fails, diagnose it from its output, `skali dev status`, and
  the logs, then fix the manifest or report the problem. Do not fall back to a
  hand-built setup without the user's agreement. Commands that discard local
  data, such as resetting the platform (which affects every local project) or
  purging a project, need the user's confirmation.

## Establish the target and read its references

Run these reference commands from the project directory, using the same
`--remote` override when selecting a target explicitly:

- `skali skill read architecture` before designing or porting an application.
- `skali skill read manifest` before editing any part of a manifest. Its field
  reference explains every key, default, expression boundary, and deployment
  restriction; examples supplement that reference.
- `skali skill read cli` for the command overview. Then load only the command
  needed, such as `skali skill read cli deploy` or `skali skill read cli cluster
  node remove`, and its linked workflow guide. Each command page includes all
  its flags, inherited flags, defaults, aliases, and flag combinations. Group
  pages list their children; `skali skill read cli workflows` lists shared
  guidance for deployment, dev, backups, data, access, and cluster operations.

Discover focused pages through the target's overview. If an older release's
overview is a combined reference without those links, use that reference;
do not substitute pages from a newer release. Keep the overview and selected
pages in context for the current target instead of loading every command page.

For a large reference, save the command output to a temporary file, retain its
provenance header, and search for the relevant headings. Reread from the CLI
when the target or release changes.

The reference header names the answering release, target, selection source,
and whether its version was verified online or selected offline. The
checkout binding takes precedence over the current remote. With no target,
the installed CLI answers. `--manifest PATH` selects another checkout.
Never guess manifest fields or infer the answering release from `skali version`.
The website and its floating editor schema may describe a stable release older
than a target running a prerelease. Prefer the target's embedded references;
use the release-specific schema URL described in its manifest reference.

Command selectors and confirmation flags vary by command. Do not carry a flag
from a similarly named command without checking its entry. In particular,
distinguish host commands, container exec, SQL sessions, and journal operations;
distinguish CLI upgrades, local platform upgrades, and managed cluster upgrades.
Use the user's existing authorization for changes, and confirm destructive
scope before bypassing a command's data-loss prompt.

Carry the target and release through authoring and validation. If either
changes, reread the references before continuing. A previously read reference
does not establish the version for the rest of an agent session.

## Validate against the same target

After manifest changes, run `skali validate` before finishing, with the same target selectors.
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
