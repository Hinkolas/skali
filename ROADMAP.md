# Skali roadmap

Where skali is heading, roughly in the order it matters. Shipped work is
in the [release notes](https://github.com/Hinkolas/skali/releases); the
details and discussion of each item live in its linked issue. Issues
labeled `status: planned` are next up, `status: deferred` ones come later.

Rule of thumb: production confidence first, then make the Studio honest,
then features. Everything is dogfooded on a real cluster before it counts.

## Now: v0.1.0, the first stable release

- Rehearse the release end to end from released binaries
  ([#110](https://github.com/Hinkolas/skali/issues/110)).

## Next: production confidence

The goal: put a paying workload on skali and sleep.

- Platform health for cluster admins, with a System status page in the
  Studio, in 0.2.0 ([#108](https://github.com/Hinkolas/skali/issues/108)).
- Back up skali's own state off-cluster and make `skali cluster restore`
  real ([#111](https://github.com/Hinkolas/skali/issues/111)).
- Continuous PostgreSQL backup with point-in-time restore
  ([#112](https://github.com/Hinkolas/skali/issues/112)).
- Registry retention and garbage collection
  ([#129](https://github.com/Hinkolas/skali/issues/129)).
- Replicate the object store's metadata with the fleet
  ([#114](https://github.com/Hinkolas/skali/issues/114)).
- Change cluster settings without repeating first-time initialization
  ([#120](https://github.com/Hinkolas/skali/issues/120)).
- A security pass over releases and the runtime
  ([#121](https://github.com/Hinkolas/skali/issues/121)).
- Install a multi-node production cluster and run the live checks that
  have never run on real hardware
  ([#119](https://github.com/Hinkolas/skali/issues/119)), then write the
  recovery runbook and try each path once
  ([#118](https://github.com/Hinkolas/skali/issues/118)).
- A manifest reference and an operations guide
  ([#126](https://github.com/Hinkolas/skali/issues/126)).
- Move a real production workload onto skali and leave it there.

## Later

### Operations

- Minimal alerting for operators
  ([#116](https://github.com/Hinkolas/skali/issues/116)).
- Roll skalid and the Studio without dropping in-flight runs or traffic
  ([#115](https://github.com/Hinkolas/skali/issues/115)).
- Drain, replace, and retire nodes without hand surgery
  ([#123](https://github.com/Hinkolas/skali/issues/123)).
- Resource defaults, caps, and usage per environment priority
  ([#117](https://github.com/Hinkolas/skali/issues/117)).
- Reclaim volumes and namespaces that leave a manifest
  ([#113](https://github.com/Hinkolas/skali/issues/113)).
- Updates for clusters without access to github.com
  ([#122](https://github.com/Hinkolas/skali/issues/122)).
- Version pin bumps (k3s, CNPG, SeaweedFS, Traefik, cert-manager, Longhorn)
  as a routine, with the cluster end-to-end suite as the gate.
- Multi-edge traffic distribution and DNS guidance.
- Log retention and shipping beyond the kubelet defaults.
- High availability for skalid itself, once a second cluster or a real
  outage motivates it.

### Studio

- A cluster dashboard as the home page
  ([#125](https://github.com/Hinkolas/skali/issues/125)).
- The hidden project and service pages: logs, environment, activity, and
  the rest ([#124](https://github.com/Hinkolas/skali/issues/124)).
- A web terminal and rollback from the Studio.

### Product

- Push-to-deploy: git integration and a managed builder, so a push deploys
  without a laptop CLI.
- API and CI tokens.
- Scheduled jobs and one-off commands per application.
- Sidecars or several processes per application, if a real app needs it.
- Bucket policies: public read, versioning, object quotas.
- Environment cloning, and preview environments per branch.
- Database access from outside the cluster through a per-database hostname
  ([#90](https://github.com/Hinkolas/skali/issues/90)).
- Restore a remote backup locally
  ([#31](https://github.com/Hinkolas/skali/issues/31)).
- Rotate credentials on a schedule
  ([#128](https://github.com/Hinkolas/skali/issues/128)).
- More database engines: Valkey first, then whatever a real project needs.
- Redirect domains, and buckets that share an application's hostname under
  a path.
- Autoscaling, and placement and priority policies across environments.
- Notifications (deploy finished, run failed) to chat or a webhook.
- A templates and examples catalog, and Compose import.
- A LAN or HTTP-only installation profile for home and office clusters.
- Queued jobs on external workers, and cluster-wide add-ons
  ([#93](https://github.com/Hinkolas/skali/issues/93)).

### Developer experience

- A prebaked dev node image for a faster first `skali dev`
  ([#127](https://github.com/Hinkolas/skali/issues/127)).
- Simplify what grew crooked: CLI command grouping, flag names, and error
  wording, once the Studio shows what is actually used.
- Faster and steadier end-to-end suites that stay runnable on a laptop.

## Not planned

- Installing onto a Kubernetes cluster skali does not own.
- Editing definitions in the Studio: `skali.yaml` stays the only source of
  truth, and the Studio is a status and operations surface.
- Supporting the pre-v2 schema or any migration from it.
- A second authoritative state plane. New features are service modules,
  policies, run kinds, or client workflows.
