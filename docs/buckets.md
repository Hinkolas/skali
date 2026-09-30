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

Outputs: `endpoint`, `name`, `region` (plain) and `access_key`,
`secret_key` (secret). The application waits until the bucket is
provisioned and starts with the outputs injected; the access identity is
scoped to exactly its bucket, and revealing credentials never passes
through definitions, revisions, or logs.

## The v1 surface

Buckets are private with a hard storage quota. `visibility: public-read`,
`versioning: enabled`, lifecycle rules, `quotas.objects`, and
`quotas.maxObjectSize` are authored vocabulary already, but they arrive
with later policies and are rejected at deploy with a clear error until
then.

## Quotas

`quotas.storage` is enforced on the observation cadence: when usage reaches
the quota the bucket turns read-only until space is freed, and the service
reports degraded with its usage. Enforcement is approximate by up to one
poll interval plus in-flight uploads, and usage counts
deleted-but-unvacuumed bytes until SeaweedFS compacts them.

## Endpoints

Applications reach their buckets on the internal endpoint by default
(`http://seaweed-s3.skali-platform.svc.cluster.local:8333`). When the
installation configures a public S3 domain (`endpoints.s3` at
`skali cluster init` or `upgrade`), the `endpoint` output switches to
`https://<domain>` through the edge with automatic TLS. In-cluster traffic
hairpins through the edge too, so every signature is computed for the one
public host.

A public endpoint is not a public bucket. The endpoint makes the S3 API
reachable; every bucket stays private and every request still needs a
valid signature. What the public endpoint enables is the browser flow
below, where the signature travels in the URL.

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
