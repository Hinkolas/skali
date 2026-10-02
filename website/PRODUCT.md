# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Primary: developers on small teams and agencies who ship several apps to their
own VPS or servers and want PaaS-style deploys without hiring or becoming a
platform engineer. They arrive evaluating whether skali can replace the
scripts, compose files and hand-run Kubernetes they would otherwise maintain.

Secondary: solo developers and homelabbers running side projects on hardware
they own. Welcome, but the site leads with the team case.

## Product Purpose

skali is a self-hosted application platform. One `skali.yaml` describes an
application, its Postgres databases and its S3 buckets; skali builds it, runs
it with TLS routes, health checks and rollouts, and hands out the credentials.
The site at skali.dev exists to get the right visitor to install the CLI and
try it; later it will also host the user documentation (today the docs links
point at the GitHub repository).

Success: a qualified visitor understands what skali replaces, trusts that it
is honest about its alpha status, and runs the install command.

## Positioning

All four are confirmed and should be stressed together; no single competitor
(Coolify, Dokploy, CapRover, Kamal) offers the combination:

- **One manifest, laptop to production.** The same `skali.yaml` runs on a real
  local cluster (`skali dev`) and on your servers (`skali deploy`). What works
  locally is what ships.
- **Data is part of the app.** Managed Postgres (CloudNativePG) and S3 buckets
  (SeaweedFS) are declared next to the code, provisioned on deploy, and
  injected as environment variables. Nobody copies credentials.
- **Real k3s, hidden.** Proven parts (k3s, Traefik, Let's Encrypt,
  CloudNativePG, SeaweedFS) installed and operated for you. You never touch
  Kubernetes.
- **Agent-ready.** `skali skill install` gives coding agents the platform's
  rules, with manifest, CLI and architecture references served at the
  project's release through `skali skill read`. Not yet featured on the site;
  worth surfacing.

## Operating Context

- Workflow: develop (`skali dev`), deploy (`skali deploy`), promote
  (`skali deploy --from staging`), recover (`skali rollback`).
- Servers become nodes with `sudo skali cluster`; single node or many, macOS
  via Lima.
- Two interfaces: the CLI and Skali Studio (web UI with users, 2FA and
  per-project roles). The site pictures Studio through hand-built static mocks
  in `src/lib/mock/` that mirror Studio's tokens.
- Install: `curl -fsSL https://skali.dev/install.sh | sh` (macOS and Linux,
  amd64 and arm64, checksum verified). The site serves `install.sh` and the
  JSON schemas verbatim (see README.md).

## Capabilities and Constraints

- Capabilities on the site must match the README and ROADMAP: apps from a
  Dockerfile or image, blue-green by default (rolling, recreate), release
  commands, routes with automatic TLS, environments with encrypted values,
  promotion, rollbacks, backups to your own S3, cluster upgrades, diagnose
  and repair.
- Status: preparing v0.1.0-alpha.1. Nothing is guaranteed before v1.0.0; the
  site must say it is early alpha and link the known limitations. Never imply
  production readiness, stability guarantees or hostile-workload isolation.
- Technical: SvelteKit + Tailwind v4, fully prerendered with
  `adapter-static` to GitHub Pages. No server code; anything a page needs is
  known at build time. The navigation shows the newest release tag.
- Terminology: the product is **skali**, always lowercase, including at the
  start of a sentence. The web UI is **Skali Studio**. The manifest is
  `skali.yaml`.

## Brand Commitments

- Name and casing as above; the logo mark is `src/lib/components/LogoMark.svelte`.
- Voice: plain, factual, short declarative sentences; concrete commands over
  adjectives; honest about limits.
- Open source under the Apache License 2.0; the repository is
  github.com/Hinkolas/skali.

## Evidence on Hand

- Real: the README, ROADMAP, docs in `../docs/`, the CLI commands, the
  example in `../examples/hello-world`, and Studio screens (rendered as mocks).
- Absent and must not be fabricated: users, customer logos, testimonials, case
  studies, benchmarks, GitHub star or download counts, uptime figures.
- Commercial: free and open source. No paid tier, hosted offering or support
  plan exists or may be implied.

## Product Principles

1. **Show the real thing.** Commands, manifests and Studio screens that exist
   beat claims about them.
2. **Honest alpha.** State the status and limitations plainly; credibility
   comes from candor, not borrowed proof.
3. **Replace work, not tools.** Frame skali by the scripts, runbooks and
   platform chores it removes.
4. **The platform disappears.** Kubernetes is the foundation, never the
   pitch or the user's burden.
5. **One manifest is the throughline.** Local development, data, deploys and
   operations all trace back to `skali.yaml`.
