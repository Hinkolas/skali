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

Applications reach their buckets on the internal endpoint by default. When
the installation configures a public S3 domain (`endpoints.s3` at
`skali cluster init` or `upgrade`), the `endpoint` output switches to
`https://<domain>` through the edge with automatic TLS; presigned URLs then
resolve for browsers too. Local development always uses the in-cluster
endpoint (a documented parity boundary).

## Topology

Production derives the store's shape from the object-storage-capable node
count: three raft masters when three or more nodes carry the capability
(else one), one volume server per capable node using the node's disk, and
one replica on a different node when the fleet has two or more (none on a
single node). Local development runs one all-in-one process that stops,
data retained, while no running project uses buckets, and resumes with the
next deployment.

## Deleting a bucket

Removing a bucket from the definition is a destructive change: the plan
marks it, deploy requires explicit confirmation, and teardown removes the
access identity, the bucket, and its objects immediately. The platform
store itself remains for the next bucket.
