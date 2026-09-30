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
service's outputs (its endpoints and credential version) into the pod
template, so when an endpoint is republished or a credential rotates,
exactly the applications referencing that service roll and start with the
new values; nothing else restarts.

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
They converge when the bucket is provisioned and on every observation
pass; anything found changed is reset and the service reports a
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
- **When it applies.** Usage is read on the observation cadence, so an
  overshoot of up to one interval plus whatever was in flight lands before
  the flag takes effect.
- **What the flag does.** At or over the quota the bucket refuses uploads
  (PUT, multipart) from the application's identity; reads, listings and
  deletes keep working, so space can always be freed. The service reports
  degraded with its usage. Once usage drops under the quota the next
  observation lifts the flag.
- **What is reported.** The usage diagnostic carries the live bytes and an
  approximate entry count: a large object is stored as several entries, so
  the count is an upper bound on objects, not an object count. The store's
  disk footprint (deleted bytes included until compaction) is tracked
  separately and never charged.

## Endpoints

Two endpoint outputs exist. `internal_endpoint` is always the in-cluster
gateway (`http://seaweed-s3.skali-platform.svc.cluster.local:8333`): the
address for the application's own traffic. `endpoint` is the address to
sign URLs for: it equals the internal endpoint until the installation
configures a public S3 domain (`endpoints.s3` at `skali cluster init` or
`upgrade`), when it switches to `https://<domain>` through the edge with
automatic TLS. Both carry the same credentials, so an application can talk
to the store directly and still hand browsers URLs signed for the public
host. Signing against `endpoint` and everything else against
`internal_endpoint` is the intended split; using `endpoint` for both works
too, hairpinning through the edge.

A public endpoint is not a public bucket. The endpoint makes the S3 API
reachable; every bucket stays private and every request still needs a
valid signature. What the public endpoint enables is the browser flow
below, where the signature travels in the URL.

The domain is reconciled like everything else the store owns. Changing
`endpoints.s3` (re-run `skali cluster init --config` with the new value)
replaces the route and the certificate in place: the new host serves as
soon as its certificate is issued, and nothing of the old domain remains.
Clearing it removes the routes and the certificate on the next pass, so the
old host stops answering, and `endpoint` falls back to the in-cluster
gateway. Either way the applications that reference the bucket roll to pick
up the new `endpoint`, and URLs signed for the previous host stop working at
that moment; bucket data is never touched by an endpoint change.

Local development (`skali dev`) has no public domain: applications running
in the cluster get the internal endpoint, while applications run on the
host through a `dev` block get a loopback address (`http://127.0.0.1:30510`
by default; `SKALI_DEV_LOOPBACK_PORT_BASE` shifts the range). Browsers on the developer
machine can reach the loopback address, not the in-cluster one, so an
application that signs URLs for browsers needs to sign for the loopback
address locally; the `file-sharing` example reads an optional
`S3_PUBLIC_ENDPOINT` for exactly that.

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
platform is reset on the next observation and reported as
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

Production derives the store's shape from the object-storage-capable node
count: three raft masters when three or more nodes carry the capability
(else one), one volume server per capable node using the node's disk, and
one replica on a different node when the fleet has two or more (none on a
single node). The shape follows the fleet as it grows: adding a second
capable node turns replication on and existing volumes gain their copy on
the store's maintenance cadence (about every twenty minutes, one copy per
volume per pass); a third node forms the master quorum. Until the copies
exist the bucket reports how many volumes are still short. Shrinking is
never automatic: a node that leaves keeps the recorded shape and the
bucket reports the missing member and the missing copies until the node
returns or an operator adjusts the store (see
[known limitations](limitations.md#shrinking-the-object-store-is-manual)).
Local development runs one all-in-one process on a small volume; it stays
up with the rest of the local platform, so buckets are warm for the next
deployment and `skali dev down` keeps their data.

Every component declares resources and health probes, a filer rollout keeps
one gateway serving throughout, and disruption budgets keep a node drain
from taking the master quorum or the last filer with it. The bucket
directories live in the shared managed Postgres pool, so bucket
availability is bounded by that pool's (see
[known limitations](limitations.md#object-storage-depends-on-the-metadata-database)).

A bucket's health reads the store: unhealthy when the S3 gateway does not
answer an authenticated request (or answers an anonymous one) or no volume
server serves; degraded, with the shortfall named, when a master or volume
server is down against the recorded shape, when volumes are missing
copies, when the metadata service does not serve, or when the public
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

A snapshot of a live bucket is loosely consistent: objects written or
deleted while the copy runs may or may not be in it. Stop the environment
first (a restore does) when an exact cut matters.

A restore fences the bucket while it rewrites it. The bucket's own
identity is deleted for the duration, so the application's mirrored keys
and every presigned URL signed with them are refused, and no external
writer can land an object between the clear and the last restored one.
The quota flag is lifted for the restore's own writes and re-evaluated by
the next probe. When the bucket is restored the identity comes back with
its unchanged keypair: URLs signed before the restore work again from that
moment, because the key that signed them is the same key. Only a
credential rotation retires them for good. A restore that fails leaves the
environment down and the bucket fenced on purpose; re-running the restore
is the way forward, and a deploy that brings the environment back instead
finds the fence lifted within one probe interval.

## Deleting a bucket

Removing a bucket from the definition is a destructive change: the plan
marks it, deploy requires explicit confirmation, and teardown removes the
access identity, the bucket, and its objects immediately. The platform
store itself remains for the next bucket.
