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

Buckets are private with a hard storage quota. `visibility: public-read`,
`versioning: enabled`, lifecycle rules, `quotas.objects`, and
`quotas.maxObjectSize` are authored vocabulary already, but they arrive
with later policies and are rejected at deploy with a clear error until
then.

## What Skali owns on a bucket

A bucket's access identity is scoped to object access on exactly its
bucket: reading, writing, listing, and tagging objects, and nothing on any
other bucket. Bucket configuration is Skali's: every bucket carries a
policy Skali writes that denies its own identity the administrative
operations (bucket policy, CORS, lifecycle, versioning, ACLs, bucket
tagging), so an application holding the full keypair cannot change
settings behind the platform, and in particular cannot open the bucket to
anonymous reads. The settings Skali currently enforces are: that policy,
no CORS configuration, no lifecycle rules, and versioning not enabled.
They converge when the bucket is provisioned and on every observation
pass; anything found changed is reset and the service reports a
`configuration-drift` warning naming what was reset.

Skali speaks S3 to the store as its own platform identity for this (and
for what follows in later releases: restores, readiness checks). That
identity's keypair lives in the platform namespace and is never injected
into an environment.

## Quotas

`quotas.storage` is enforced on the observation cadence: when usage reaches
the quota the bucket turns read-only until space is freed, and the service
reports degraded with its usage. Enforcement is approximate by up to one
poll interval plus in-flight uploads, and usage counts
deleted-but-unvacuumed bytes until SeaweedFS compacts them.

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
cross-origin `PUT`. The platform store answers preflights for any origin
with `GET`, `PUT`, `POST`, `DELETE`, and `HEAD`, admits the request
headers the browser names, and exposes `ETag` on the response; the
signature, not the origin, is what authorizes a request. Declaring CORS
per bucket is planned.

[`examples/file-sharing`](../examples/file-sharing) is a runnable
reference for the whole flow (single and multipart uploads, share links,
verification), and the dev end-to-end suite replays a browser's requests
against it.

## Topology

Production derives the store's shape from the object-storage-capable node
count: three raft masters when three or more nodes carry the capability
(else one), one volume server per capable node using the node's disk, and
one replica on a different node when the fleet has two or more (none on a
single node). Local development runs one all-in-one process on a small
volume; it stays up with the rest of the local platform, so buckets are
warm for the next deployment and `skali dev down` keeps their data.

## Deleting a bucket

Removing a bucket from the definition is a destructive change: the plan
marks it, deploy requires explicit confirmation, and teardown removes the
access identity, the bucket, and its objects immediately. The platform
store itself remains for the next bucket.
