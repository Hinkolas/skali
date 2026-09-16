# Known limitations

Things skali does not do yet, stated so nobody finds out the hard way.
Each one is deliberate for now and tracked in [`ROADMAP.md`](../ROADMAP.md).

## Volumes are never reclaimed automatically

The reconciler prunes stateless objects (workloads, services, routes) that
disappear from the manifest, but never PersistentVolumeClaims and never
namespaces. Consequences:

- Removing a `volumes:` entry from an application, or removing the whole
  application, leaves its volume and data in the environment's namespace.
  Nothing uses it, nothing reports it, and its disk stays allocated until
  you delete the claim by hand:

  ```sh
  kubectl -n skali-<project>-<environment> get pvc
  kubectl -n skali-<project>-<environment> delete pvc <name>
  ```

- Renaming a volume key is a removal plus a creation: the new volume
  starts empty and the old one stays behind as above.
- `skali env remove` (a purge) is the one path that removes everything: the
  namespace goes with every volume in it.

Declared sizes are only enforced on the `longhorn` driver; on `local` the
size is advisory and volumes pin their pods to one node. See
[`storage.md`](storage.md).

## A deploy briefly needs room for two copies of an application

The default `blue-green` rollout starts the new version at full replica
count beside the old one and only switches traffic once every new replica
is ready. Consequences:

- The cluster must hold twice the application's replicas for the length of
  the deploy. When it cannot, declare `strategy: rolling` for that
  application (and keep its releases compatible with the previous one).
- Switching an application between `rolling` and `blue-green` serves both
  versions for that one rollout: the existing Deployment keeps its
  uncolored selector while the new one starts, and both match the Service
  until the switch. From then on old and new never serve together.
- Browsers holding a page from the previous version may still request
  assets only that version had. The switch shrinks the window to the time
  between the page and its asset requests; only the framework's own
  new-deployment detection (a version check and reload) closes it.
- Metrics count the pending version's pods for the duration of the deploy,
  so per-service CPU and memory read roughly double while it runs.

## Deleting a project requires purging its environments first

A project is deleted only once it has no environments: the API refuses
otherwise (409), because environment records are released by the
reconciler after a purge confirms the namespace is gone, and deleting them
any other way would orphan running workloads. Run `skali env remove <name>` for
each environment, then delete the project. A namespace that was orphaned
before this guard existed can only be removed by hand
(`kubectl delete namespace skali-<project>-<environment>`); skalid logs
`orphaned managed namespace retained` for it.

## The registry node cannot be removed

`skali cluster node remove` refuses the node that holds the managed
registry's data ("registry migration is not implemented"). Plan the seed
node as permanent, or, on the `longhorn` driver, run
`skali cluster storage-migrate` first, which moves the registry volume onto
replicated storage; the removal guard is still in place afterwards and will
be relaxed once migration between nodes is implemented.

## Disaster recovery is manual

There is no `skali cluster restore` yet. Recovering an installation on a
fresh host means reinstalling, restoring the system database from your own
backup, and re-pushing images with `skali deploy`. Preserve the original
`AUTH_SECRET` and `/var/lib/skali/installation.yaml` securely with your
system-database backup: a restored database cannot decrypt its environment
values, TOTP secrets, or backup credentials without the original key. See
[security and key recovery](security.md). Environment data (databases, buckets, volumes) is covered
by `skali backup`, but skali's own state is not backed up automatically.

## Routes whose domain does not point here yet

When you migrate a project from another host, a route's domain usually still
resolves to the old provider on the first deploy. Before it waits for a
certificate, the reconciler checks whether the domain reaches this
installation's edge: it resolves the domain and requests
`/.well-known/skali-edge` from every address with the domain as the Host
header, and recognises its own edge by the `Skali-Instance` header. A domain
that does not reach this edge is deferred. The deploy still goes green and
the application serves on every route that does point here; the run's
"Issue TLS certificate" checkpoint ends skipped with a warning naming the
domain; the ready summary prints `cert deferred · domain not pointing here
yet` and a `warning:` line; the environment status carries the verdict per
route (`edge.state`); and the console shows a "DNS pending" badge next to
the route. The Certificate object stays rendered, and cert-manager holds off
validation while its own HTTP-01 self check fails, so nothing counts against
the CA's failed-validation limits while DNS is elsewhere.

A domain that does reach this edge but fails validation keeps failing the
run at the rollout deadline with the issuance reason, exactly as before. A
probe that cannot say anything (a resolver outage) also keeps that
behaviour, so a broken probe never hides a real failure.

Migration workflow: deploy here first, test through other routes or by
pinning the domain in `/etc/hosts`, then move the A and AAAA records
together (one record left behind reads as `partial`, which still defers:
certificate authorities prefer IPv6 and would validate against the old
host). The reconciler re-probes every two minutes; when the domain arrives
it requests a fresh issuance right away, so a certificate parked in
cert-manager's failure backoff does not wait it out. No redeploy is needed.
Until then the https URL of a deferred route does not answer here. An
installation without certificates (local development) never probes.

## Builds run on your machine

Every `skali deploy` builds and pushes from the machine running the CLI;
there is no push-to-deploy or in-cluster builder. `--build auto` resolves
to `local` today.

## Single management plane

`skalid` runs as one deployment. The applications it manages keep running
if it is down, but deploys, the console, and the API are unavailable until
it is back.

## Console updates need a coordinator-managed cluster and internet egress

The System / Software update page can move a reconciled (coordinator-managed)
cluster to a newer release. The daemon's daily scan reads the GitHub releases
API, and every node downloads its new `skali-hostd` and k3s from GitHub, so
both need outbound HTTPS. When the feed cannot be reached (GitHub down, no
egress, or the repository not public) the page shows an "update servers are
offline" notice, keeps the last release it found, and retries every hour.
`SKALI_UPDATE_SCAN=false` keeps the daemon fully
offline (the page still works for settings and shows nothing to update);
`/etc/skali/hostd.env` with `SKALI_RELEASE_BASE` points node downloads at a
mirror. Legacy (version-1) installations show the available release but must
be upgraded with `skali cluster upgrade`; the local `skali dev` platform
uses one fixed cluster matching the selected release. A release change
requires `skali dev reset` and deletes its local data after confirmation. Release assets are
verified against `checksums.txt`
over TLS; there is no signature yet (see the release checklist).

Manifest backup policies are accepted but inactive: scheduling and retention are not enforced. Use manual backups. See [prerelease safety](prerelease-safety.md) for hostname ownership, restore behavior, and the fresh-install requirement.
