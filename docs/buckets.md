# Managed object storage

Skali provisions S3-compatible buckets from the project definition, backed
by a managed SeaweedFS system. A bucket is declared as a service and
connected to applications through typed output references; Skali allocates
it on the platform store, creates the bucket and a scoped access identity,
and injects the connection outputs.

```yaml
applications:
  web:
    environment:
      S3_ENDPOINT: "{{ buckets.files.endpoint }}"
      S3_BUCKET: "{{ buckets.files.name }}"
      S3_REGION: "{{ buckets.files.region }}"
      S3_ACCESS_KEY: "{{ buckets.files.access_key }}"
      S3_SECRET_KEY: "{{ buckets.files.secret_key }}"

buckets:
  files:
    quotas:
      storage: 20GB
```

Outputs: `endpoint`, `internal_endpoint`, `name`, `region` (plain) and
`access_key`, `secret_key` (secret). The application waits until the
bucket is provisioned and starts with the outputs injected; the access
identity is scoped to exactly its bucket, and revealing credentials never
passes through definitions, revisions, or logs.

Outputs are delivered to pods as environment variables, which a process
reads once at start. Skali folds a non-secret identity of each referenced
service's outputs (its endpoints, credential version, and output version)
into the pod template, so when an endpoint is republished or a credential
rotates, exactly the applications referencing that service roll and start
with the new values; nothing else restarts. The identity advances only
after the output Secret holds the new values, so a pod never starts on
values it is about to stop reading.

## The v1 surface

Buckets are private with a hard storage quota, an optional CORS policy
(`cors`, see [browser uploads](#browser-uploads-and-downloads)), and
automatic cleanup of abandoned multipart uploads
(`lifecycle.abortIncompleteUploadsAfter`, a day when unset). `visibility:
public-read`, `versioning: enabled`, `lifecycle.expireNoncurrentVersionsAfter`,
`quotas.objects`, and `quotas.maxObjectSize` are authored vocabulary
already, but they arrive with later policies and are rejected at deploy
with a clear error until then.

## What Skali owns on a bucket

A bucket's access identity is scoped to object access on exactly its
bucket: reading, writing, listing, and tagging objects, and nothing on any
other bucket. Bucket configuration is Skali's: every bucket carries a
policy Skali writes that denies its own identity the administrative
operations (bucket policy, CORS, lifecycle, versioning, ACLs, bucket
tagging), so an application holding the full keypair cannot change
settings behind the platform, and in particular cannot open the bucket to
anonymous reads. The settings Skali currently enforces are: that policy,
the declared CORS configuration (or none), no lifecycle rules on the
store itself (cleanup runs in the platform), and versioning not enabled.
They converge when the bucket is provisioned and on every storage upkeep
pass (every 15 seconds, separate from health observation so slow upkeep
never makes storage health stale); anything found changed is reset and the service reports a
`configuration-drift` warning naming what was reset.

Skali speaks S3 to the store as its own platform identity for this, for
backups and restores (below), and for the readiness checks that follow.
That identity's keypair lives in the platform namespace and is never
injected into an environment.

## Quotas

`quotas.storage` is the one enforced quota; `quotas.objects` and
`quotas.maxObjectSize` are refused at deploy rather than silently
ignored. Its guarantees, exactly:

- **What counts.** The live bytes of the bucket's objects as the volume
  servers record them: an upload counts once it lands, a delete stops
  counting on the store's next heartbeat (a few seconds), without waiting
  for the vacuum that reclaims the disk later. Replicas do not count. The
  parts of a multipart upload count while the upload is in progress and
  until it completes or is aborted.
- **When it applies.** Usage is read on the upkeep cadence (every 15
  seconds), so an overshoot of up to one interval plus whatever was in
  flight lands before the flag takes effect.
- **What the flag does.** At or over the quota the bucket refuses uploads
  (PUT, multipart) from the application's identity; reads, listings and
  deletes keep working, so space can always be freed. The service reports
  degraded with its usage. Once usage drops under the quota the next
  upkeep pass lifts the flag.
- **What is reported.** The usage diagnostic carries the live bytes and an
  approximate entry count: a large object is stored as several entries, so
  the count is an upper bound on objects, not an object count. The store's
  disk footprint (deleted bytes included until compaction) is tracked
  separately and never charged.

## Endpoints

Two endpoint outputs exist. `internal_endpoint` is always the in-cluster
gateway (`http://seaweed-s3.skali-platform.svc.cluster.local:8333`): the
address for the application's own traffic. `endpoint` is the address to
sign URLs for. Without a route it equals the internal endpoint: the bucket
is reachable only from the environments that hold a bucket (a network
policy on the gateway port admits exactly those namespaces, the platform
itself, and the edge), and only with its credentials, the way a managed
database is. A bucket route publishes the bucket on a
hostname of its own, and `endpoint` becomes that hostname's origin:

```yaml
buckets:
  files:
    route:
      domain: ${STORAGE_DOMAIN}   # or a literal hostname
      tls: automatic              # optional, automatic | optional | disabled
```

The edge serves exactly this bucket on the hostname, path-style
(`https://<domain>/<bucket>/<key>`), with a certificate issued for it and
plain HTTP redirected (`tls: automatic`, the default), served too
(`optional`), or no certificate at all (`disabled`, which publishes an
`http://` endpoint). Both outputs carry the same credentials, so an
application can talk to the store directly and still hand browsers URLs
signed for the public host. Signing against `endpoint` and everything else
against `internal_endpoint` is the intended split; using `endpoint` for
both works too, hairpinning through the edge.

A route is not a public bucket. The hostname makes the S3 API reachable
for this one bucket; the bucket stays private and every request still
needs a valid signature. What the route enables is the browser flow
below, where the signature travels in the URL. Requests for any other
bucket through the hostname are refused at the edge (404), so a hostname
never becomes a side door to the rest of the store.

The hostname goes through the same ownership and readiness checks as an
application route: it is claimed by the environment at deploy (another
environment cannot take it, and the installation's own hosts are refused),
the reconciler probes whether it reaches this installation before it waits
on a certificate, and `skali route list`, `skali route probe`, the ready
summary, and the bucket's Studio page show the certificate and DNS state
(see docs/limitations.md, "Routes whose domain does not point here yet").
Several buckets of one environment may share a hostname, each on its own
path; a bucket cannot share a hostname with an application route yet. The
variables a route references are never redacted: a hostname the edge
serves is public by construction.

Changing a bucket's route domain republishes `endpoint` and rolls the
applications that reference the bucket, and URLs signed for the previous
host stop working at that moment; there is no grace period with both
hosts live. Removing the route returns `endpoint` to the in-cluster
gateway, deletes the routers and the certificate on the next pass, and
releases the hostname once the old routers are confirmed gone. Bucket
data is never touched by a route change.

Upgrading from a release that had an installation-wide S3 endpoint
(`endpoints.s3` at `skali cluster init`): that endpoint no longer exists.
Every bucket without a route publishes the in-cluster gateway, and the
applications referencing it roll onto it. The old routers and certificate
keep serving until every bucket's outputs are confirmed and fifteen
minutes have passed since the last change, so nothing reads the old host
when it goes away; skalid releases the hostname's reservation at start so
an environment can claim it as a bucket route. Declare `route` on
each bucket that needs a public hostname and deploy before upgrading;
`skali cluster upgrade` lists the buckets still published on the old host
before it asks for confirmation. A stale `endpoints.s3` in an init config
is ignored with a warning.

Local development (`skali dev`) renders routes like production, with the
local CA issuing their certificates, so `https://<route domain>` answers
on the local edge when the domain resolves to it (`*.localhost` does).
Applications running in the cluster get the route as `endpoint`; applications
run on the host through a `dev` block get a loopback address instead
(`http://127.0.0.1:30510` by default; `SKALI_DEV_LOOPBACK_PORT_BASE`
shifts the range) because the host process cannot reach in-cluster names;
the local platform admits the host on the gateway port for that reason.
Browsers on the developer machine can reach the loopback address, so an
application that signs URLs for browsers signs for the loopback address
locally; the `file-sharing` example reads an optional `S3_PUBLIC_ENDPOINT`
for exactly that.

## Browser uploads and downloads

The intended pattern for user files: the application authorizes a request
and signs a presigned URL, the browser uploads or downloads straight
through the S3 endpoint, and the file payload never passes through the
application. Signing needs nothing beyond the connection outputs, and the
bucket credentials stay server-side: the browser only ever sees signed
URLs that expire.

Details that make signatures verify on the platform store:

- Path-style addressing (`https://<endpoint>/<bucket>/<key>`) and region
  `us-east-1`, the values every S3 client takes from the outputs.
- Sign against the published `endpoint` as-is and hand the browser exactly
  that URL. The signature covers the host, the path, and the query; a URL
  rewritten afterwards (another host, a proxy prefix, a re-encoded key)
  fails verification.
- Signed headers must be sent exactly. A common choice is to sign the
  `Content-Type` of an upload so the stored object carries the declared
  type; the browser must then send that header with the same value.
- Multipart uploads sign one URL per part (`uploadId`, `partNumber`); the
  application starts and completes the upload server-side with the ETags
  the browser collected. Abort what the browser abandons: incomplete
  uploads hold space until then.
- Verify before you trust: after an upload, a server-side `HEAD` of the
  object (size, type) is what makes it real in the application's own
  records.

Browsers send a CORS preflight (`OPTIONS`) to the endpoint before a
cross-origin `PUT`. Without a declared policy the platform store answers
preflights for any origin with `GET`, `PUT`, `POST`, `DELETE`, and
`HEAD`, admits the request headers the browser names, and exposes `ETag`
on the response; the signature, not the origin, is what authorizes a
request, so the open preflight costs nothing in access control. To limit
which pages may talk to the bucket at all, declare the policy:

```yaml
buckets:
  files:
    cors:
      allowedOrigins: [https://app.example.com]
      allowedMethods: [GET, PUT]        # default when unset
      allowedHeaders: [content-type]
      exposeHeaders: [ETag]
      maxAge: 10m
```

A declared policy is reconciled onto the bucket like its other settings:
a preflight from an origin that is not listed is refused, the listed
methods and headers are the ones admitted, and a change behind the
platform is reset on the next upkeep pass and reported as
`configuration-drift`.

Multipart uploads a browser started and never completed keep their parts
on the store, and those parts count against the quota. The platform
aborts uploads older than `lifecycle.abortIncompleteUploadsAfter` on the
observation cadence, one day when the bucket declares nothing; an upload
that runs longer than that is aborted mid-way, so set it above the longest
upload the application expects.

[`examples/file-sharing`](../examples/file-sharing) is a runnable
reference for the whole flow (single and multipart uploads, share links,
verification), and the dev end-to-end suite replays a browser's requests
against it.

## Topology

Production derives the store's shape from the fleet: three raft masters
when three or more nodes carry the object-storage capability (else one),
one volume server per capable node using the node's disk, one replica on
a different node when two or more nodes carry the capability (none on a
single node), and two filer/S3 gateways as soon as two nodes carry the
capability (one on a single node). Filers keep no state of their own but
answer every S3 request with round trips to the metadata database, so they
run on the object-storage nodes too, never on a distant node that merely
has room: a listing walks the bucket one directory at a time, and each
directory costs the filer's network distance to the database.
The shape follows the fleet as it grows: a second capable node brings the
second gateway and turns replication on, and a third forms the master
quorum.
The recorded shape is the desired state; on every reconcile pass the
platform compares it with the store itself (the bucket path's replication
setting and each volume's placement) and moves whatever still differs, so
a move that failed, stopped half way, or was interrupted by a restart is
retried until every volume carries the recorded setting. The copies
themselves are created on the store's maintenance cadence (about every
twenty minutes, one copy per volume per pass). Until then the bucket
reports how many volumes still carry the previous setting and how many are
still short of a copy. Shrinking is
never automatic: a node that leaves keeps the recorded shape and the
bucket reports the missing member and the missing copies until the node
returns or an operator adjusts the store (see
[known limitations](limitations.md#shrinking-the-object-store-is-manual)).
Local development runs one all-in-one process on a small volume; it stays
up with the rest of the local platform, so buckets are warm for the next
deployment and `skali dev down` keeps their data.

Every component declares resources and health probes, a filer rollout keeps
one gateway serving throughout, and disruption budgets keep a node drain
from taking the master quorum or the last filer with it. Masters and
filers must spread across nodes: the rule is required, not preferred, so
two replicas never share a node while the fleet has room to keep them
apart, and a node loss takes at most one master and one gateway. A
replica that cannot be placed stays pending and the bucket reports the
component below its recorded shape; a replica scheduled onto a survivor
during a node outage stays there when the node returns, and the bucket
then names the node the pods share until one of them is deleted and
rescheduled apart. What a node failure means at each fleet size is spelled
out in [known limitations](limitations.md#node-failure-tolerance-of-the-object-store).
The bucket directories live in the shared managed Postgres pool, so bucket
availability is bounded by that pool's (see
[known limitations](limitations.md#object-storage-depends-on-the-metadata-database)).

A bucket's health reads the store: unhealthy when the S3 gateway does not
answer an authenticated request (or answers an anonymous one) or no volume
server serves; degraded, with the shortfall named, when a master or volume
server is down against the recorded shape, when volumes are missing
copies or still carry a previous replication setting, when replicas of a
component share a node, when the metadata service does not serve, or when the public
endpoint's certificate is not issued yet (presigned URLs for the public
host fail TLS until it is).

## Backups and restores

A snapshot copies every object of the bucket into the backup target, with
the metadata that travels on an object: the content type, encoding,
disposition, language and cache headers, the application's own user
metadata, and its tags. A restore puts all of it back, so restored files
download under the name and type they were stored with. Both directions
run as the platform's own S3 identity, so a bucket that is full (read-only
for its application) still backs up, and a restore never depends on the
application's keys.

A backup first counts the bucket, so the run can show real progress.
The count walks the bucket one directory level at a time, eight levels at
once: the gateway answers a recursive listing by visiting directories one
after another, each visit a round trip to the metadata database, which
for an application that keeps every upload in its own directory meant one
round trip per object. The restore's clear of the live bucket walks it the
same way. Listings of the backup target stay flat, since a hosted target
bills every listing request.

Objects copy with bounded concurrency, 16 at a time by default
(`SKALI_BACKUP_COPY_CONCURRENCY` on the daemon; lower it for a target that
throttles parallel requests). The copy is bound by round trips to the
target rather than by bandwidth, so a bucket of many small objects moves
in minutes instead of tens of minutes. A transient failure on one object (a
server error, throttling, a dropped connection) is retried a few times
with backoff before it fails the run; a denied or invalid request fails the
run at once with the object's key in the error. A restore clears the live
bucket with batched deletes before the copy back.

A snapshot of a live bucket is loosely consistent: objects written or
deleted while the copy runs may or may not be in it. An object the listing
named that is gone by the time the copy reaches it is skipped, and the run
notes how many were skipped. Stop the environment first (a restore does)
when an exact cut matters.

Restoring an immutable snapshot is stricter: a missing object or an object count
that differs from its manifest fails the restore and keeps the bucket fenced.
Only live backups skip objects that disappear during copying.

A restore fences the bucket while it rewrites it. The bucket's own
identity is deleted for the duration, so the application's mirrored keys
and every presigned URL signed with them are refused, and no external
writer can land an object between the clear and the last restored one.
The quota flag is lifted for the restore's own writes and re-evaluated by
the next probe. When the bucket is restored the identity comes back with
its unchanged keypair: URLs signed before the restore work again from that
moment, because the key that signed them is the same key. Only a
[credential rotation](#credential-rotation) retires them for good. A
restore that fails leaves the environment down and the bucket fenced on
purpose; re-running the restore is the way forward, and a deploy that
brings the environment back instead finds the fence lifted within one
probe interval.

## Credential rotation

`skali bucket rotate <key>` (or the bucket's "Rotate keypair" button in
the Studio) issues a new keypair for one bucket without touching its
data. The rotation is a journaled run of kind `rotation` and needs
`maintain` on the environment and a recent login, like a reveal. What
happens, in order:

1. A new keypair is issued and the store accepts both: the new one and
   the one it replaces.
2. The environment's outputs carry the new keypair and the credential
   version advances, so exactly the applications referencing the bucket
   restart with it (`blue-green` brings up the new color before the
   switch, `rolling` rolls). The run succeeds once every consumer runs
   only with the new keypair.
3. After the overlap window the previous keypair is retired for good.
   Requests signed with it, including every presigned URL it signed, fail
   from that instant on. The retirement happens on the platform's own
   clock, whether or not the run succeeded.

`--retire-after` sets the window (`1h` by default, `1m` at least, `7d` at
most, the presign maximum). It must cover the consumer restart plus the
longest URL the application issues; an application that signs day-long
download links needs a day-long window. `1m` is the setting for a key
known to be leaked: until the consumers have restarted, requests they sign
with the old keypair may already be refused. Rotating again inside the
window retires the older keypair at once; there is never more than one
previous keypair.

Only pods started after the rotation hold the new keypair. Anything that
fetched the keypair earlier must fetch it again: a `skali dev` host run
picks it up at its next start, and a copy taken through "Reveal keypair"
is simply stale.

Rotation and restore exclude each other: a bucket a restore is rewriting
cannot be rotated until the restore has finished (`bucket_fenced`), and a
restore started inside a window brings back both keypairs when it lifts
the fence, with the retirement still on schedule. The connection
projection (the Studio's connection panel, the connection API) shows the
credential version and, inside a window, the instant the previous keypair
retires.

Databases rotate the same way with a login role instead of a keypair; see
[databases.md](databases.md#credential-rotation).

## Deleting a bucket

Removing a bucket from the definition is a destructive change: the plan
marks it, deploy requires explicit confirmation, and teardown removes the
access identity, the bucket, and its objects immediately. The platform
store itself remains for the next bucket.
