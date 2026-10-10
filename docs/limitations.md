# Known limitations

Things skali does not do yet, stated so nobody finds out the hard way.
Each one is deliberate for now; the larger directions are in
[`ROADMAP.md`](../ROADMAP.md) and the concrete work in
[issues](https://github.com/Hinkolas/skali/issues).

## Volumes are never reclaimed automatically

The reconciler prunes stateless objects (workloads, services, routes) that
disappear from the manifest, but never PersistentVolumeClaims and never
namespaces. Consequences:

- Removing a `volumes:` entry from an application, or removing the whole
  application, leaves its volume and data in the environment's namespace.
  Nothing uses it, nothing reports it, and its disk stays allocated until
  you delete the claim by hand:

  ```sh
  kubectl -n skali-<environment-id> get pvc
  kubectl -n skali-<environment-id> delete pvc <name>
  ```

- Renaming a volume key is a removal plus a creation: the new volume
  starts empty and the old one stays behind as above.
- `skali env remove` (a purge) is the one path that removes everything: the
  namespace goes with every volume in it.

Declared sizes are only enforced on the `longhorn` driver; on `local` the
size is advisory and volumes pin their pods to one node. See
[`storage.md`](storage.md).

## Environments cannot talk to each other

Every environment namespace carries an ingress default-deny network policy.
A pod is reachable from the other pods of its environment (by Service name,
`<app-service>` or `<app-service>.skali-<environment-id>.svc`) and from the
edge for the routes it declares, and from nothing else: not another
environment of the same project, not another project, not a process on a
cluster node. Consequences:

- Two environments that need to exchange traffic must do so through their
  public routes; there is no manifest setting yet that widens an
  environment's scope to its project or the cluster, and no way to limit
  a route to source addresses. Both are tracked in #93.
- Egress is not restricted: a pod may still open connections to the
  internet, the cluster DNS, and every service that admits it (its own
  database pool and the S3 gateway, which admit claim holders only).
- Existing environments receive the policy on their first reconcile pass
  after the upgrade that introduced it, without a redeploy; traffic between
  environments that worked before stops then. The pass records one
  `reconcile` run per environment with the applied policy.

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
(`kubectl delete namespace skali-<environment-id>`); skalid logs
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
installation's edge: it resolves the domain from inside the cluster and
requests `/.well-known/skali-edge` from every address with the domain as the
Host header, and recognises its own edge by the `Skali-Instance` header. A
domain that does not reach this edge is deferred. The deploy still goes
green and the application serves on every route that does point here; the
run's "Issue TLS certificate" checkpoint ends skipped with a warning naming
the domain; the ready summary prints `cert deferred · domain not pointing
here yet` and a `warning:` line; the environment status carries the verdict
per route (`edge.state`, `edge.deferred`); and the Studio marks the run
(`1 route deferred` on the run card, `succeeded · with warnings` in the run
panel) and the service (`DNS pending` beside its health, and on the route).
The Certificate object stays rendered, and cert-manager holds off
validation while its own HTTP-01 self check fails, so nothing counts against
the CA's failed-validation limits while DNS is elsewhere.

The same applies when a route's domain changes on an existing route (the
usual case: the first deploy runs under a staging domain, the production
domain comes with a later deploy while it still points at the old host).
The certificate on hand was issued for the old domain, so for the new one it
counts as unissued: the deploy defers the same way, and the old certificate
keeps serving the old domain until the new one arrives. The checkpoint and
the Studio name the domain it still serves (`issued for`). Once the new
domain reaches this edge but its issuance fails, the deploy gates and fails
exactly like a first issuance would.

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
cert-manager's failure backoff does not wait it out, and records the
arrival as a `reconcile` run ("Domain arrived"). A second `reconcile` run
closes the story once the certificate is issued, or fails with
cert-manager's reason when the attempt after the arrival fails. No redeploy
is needed. `skali route probe` (or "Probe now" beside the `DNS pending`
badge in the Studio) checks the domains right now, prints the verdict per
resolved address, and counts a domain that answers here as arrived, which
also asks for one fresh issuance of a certificate that keeps failing. Until
then the https URL of a deferred route does not answer here. The verdict
`unresolved` can lag public DNS by the zone's negative TTL when the name
was looked up in the cluster before it had an address. The local platform
never probes: `*.localhost` cannot resolve to its edge from inside the
cluster, so its routes gate on issuance alone, which the private CA
issuer completes in seconds.

## A bucket route serves one bucket, on its own hostname

A bucket's `route` publishes exactly that bucket: the edge matches the
bucket's path on the hostname and answers 404 for anything else, and a
presigned URL for another bucket through that hostname fails. Several
buckets of one environment may share a hostname, but a bucket cannot share
a hostname with an application route (`example.com` for the app,
`example.com/storage` for the bucket), and the bucket's path under the
hostname is its store name, not a path of your choosing. Both the shared
hostname and a configurable path are planned; until then give the bucket
a hostname of its own. The deferral, probing and `skali route probe`
behaviour above applies to bucket routes unchanged.

## Shrinking the object store is manual

The managed object store grows with the fleet (a second object-storage
node turns replication on, a third forms the master quorum) but never
shrinks on its own: removing a capable node leaves the recorded shape in
place, and every bucket reports the missing member and the volumes that
lost a copy until the node returns. Moving the store to a smaller shape
(fewer masters, a lower replication) is an operator task for now; the
store keeps serving from the remaining copies in the meantime. Moving
existing volumes to a grown replication setting runs under the store's
maintenance lock, so while the maintenance script holds it the move waits
for the next reconcile pass; the bucket names the volumes still on the
previous setting until it lands.

## Node-failure tolerance of the object store

What the managed object store survives depends on how many nodes carry
the object-storage capability (every store component, gateways included,
runs only on those); the platform never promises more than they can hold.

- **One node.** No node-failure tolerance: one master, one gateway, one
  volume server, no replication. Everything goes down with the node and
  comes back with it; nothing is lost that the disk keeps. Rollouts are
  still zero-downtime (the new gateway is ready before the old one goes).
- **Two nodes.** Data survives the loss of either node: every volume keeps
  a copy on the other node, and the two gateways run one per node, so S3
  keeps answering. Metadata does not: two nodes cannot form a raft
  quorum, so the store runs a single master, and losing the node that
  carries it stops the store (reads and writes) until that node returns.
  The master's state lives on that node's disk, so it does not move to
  the survivor. A two-node fleet is therefore durable, not highly
  available.
- **Three or more nodes.** The master quorum tolerates one node away, the
  gateways and bytes as above; the store itself survives any single node
  failure. Replication stays at one extra copy, so two simultaneous node
  failures can lose data.

The spread rules hold these shapes: two replicas of a component are never
placed on the same node while another eligible node exists. After a node
failure a replacement is scheduled onto a surviving node; when the failed
node returns the replacement is not moved back, and the bucket reports
the node the replicas share until one pod is deleted and rescheduled
apart. On a fleet too small for the recorded shape (a capable node
removed from a three-master store), the extra replica stays pending and
the bucket reports the component below its shape rather than placing two
members on one node.

In every size the buckets also depend on the metadata database below.

## Object storage depends on the metadata database

Every bucket's directory (which objects exist, where their bytes live)
is kept by the store's filer in the shared managed Postgres pool, while
the bytes themselves live on the volume servers with SeaweedFS's own
replication. Replicated bytes alone do not keep buckets available: if the
metadata database is unreachable, no object can be read or written even
though nothing is lost, and the bucket reports `store-degraded` with the
metadata service named. Availability of buckets is therefore bounded by
the availability tier of the shared pool, and a restore of the system
database restores the bucket directories with it. The store claims that
pool at the `single` tier today, so even a three-node fleet whose masters,
gateways and bytes all survive a node loses its buckets while the pool's
one Postgres pod is away; moving the metadata claim to a replicated tier
once the fleet can hold one is tracked on the roadmap.

## Changes behind the platform are repaired on a schedule

Skali writes the objects behind each database and bucket, and puts them
back when something outside it changes or removes them. It does not watch
them, so the repair runs on a schedule:

- A bucket's settings (its policy, CORS, lifecycle rules, and versioning)
  are reset within 15 seconds, by storage upkeep.
- The connection Secret in the environment's namespace, the credentials
  behind it, a bucket's access identity, and the bucket itself are
  repaired within 10 minutes. They are also checked at once when a
  deploy, rollback, or restart begins, and when the environment's health
  turns degraded or unhealthy, so a pod that cannot start for a deleted
  connection Secret does not wait out the interval.

## Bucket snapshots are loosely consistent

`skali backup` copies a bucket object by object while the application may
still be writing: objects added or deleted during the copy may or may not
be in the snapshot. An object that was listed but deleted before the copy
reached it is skipped and counted in the run's log rather than failing the
backup. Stop the environment first when an exact cut matters (a restore
does). Volumes and databases have their own notes in
[prerelease safety](prerelease-safety.md).

## Builds run on your machine

Every `skali deploy` builds and pushes from the machine running the CLI;
there is no push-to-deploy or in-cluster builder. `--build auto` resolves
to `local` today.

## Single management plane

`skalid` runs as one deployment. The applications it manages keep running
if it is down, but deploys, the Studio, and the API are unavailable until
it is back.

## Studio updates need a coordinator-managed cluster and internet egress

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

Automatic backups are a per-environment setting with one schedule per environment; the cron expression is evaluated in UTC and there is no time zone setting yet. Retention only removes snapshots the schedule took and always keeps the newest; manual snapshots stay until deleted. A schedule on an environment whose revision declares no database, bucket, or volume is kept but skips every fire (the Studio marks it as having nothing to back up); snapshots start once a deploy adds one. One backup runs at a time per installation. See [prerelease safety](prerelease-safety.md) for hostname ownership, restore behavior, and the fresh-install requirement.
