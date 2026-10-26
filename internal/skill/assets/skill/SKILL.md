---
name: skali
description: Author Skali manifests, build and deploy applications, and operate Skali through its CLI. Use for skali.yaml or skali.yml, local development, environments and values, routes, managed PostgreSQL and S3, backups, access, cluster installation or maintenance, and troubleshooting Skali commands.
---

<!-- Managed by "skali skill install"; local edits are overwritten on reinstall. -->

# Skali

Skali runs container applications with managed PostgreSQL and S3 storage,
configured in a project manifest. Use the CLI to establish the release and
read its exact grammar and platform behavior before designing or editing.

## Establish the target and read its references

Run these reference commands from the project directory, using the same
`--remote` override when selecting a target explicitly:

- `skali skill read architecture` before designing or porting an application.
- `skali skill read manifest` before editing any part of a manifest. Its field
  reference explains every key, default, expression boundary, and deployment
  restriction; examples supplement that reference.
- `skali skill read cli` before using skali commands. Start with the command
  index, then read the relevant workflow and full command entry, including
  inherited flags, defaults, aliases, and flag combinations. It covers local
  development, deployment/promotion/rollback, logs and runs, exec and SQL,
  values, backups, credential rotation, access and environments, remotes,
  cluster lifecycle, upgrades, and completions.

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
