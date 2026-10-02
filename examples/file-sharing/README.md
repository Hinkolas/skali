# file-sharing

The presigned-URL reference application, and the example that exercises
most of the manifest surface (resources, autoscaling, placement, rollout,
release command, a project-isolated database, a bucket).

The application authorizes a request and signs an S3 URL; the browser
then uploads or downloads straight through the bucket's endpoint. The file
payload never passes through the application, and the bucket credentials
never leave the server: the browser only ever sees signed URLs.

```
browser ──POST /api/uploads──▶ app ──(record + sign)──▶ { url, headers }
browser ──PUT <signed url>────────────────────────────▶ bucket endpoint
browser ──POST /api/uploads/{id}/complete──▶ app ──HEAD object──▶ bucket
browser ──GET /files/{id}──▶ app ──302──▶ <signed GET url> ──▶ bucket endpoint
```

## Run it

```sh
cp .env.example .env     # set UPLOAD_TOKEN
skali dev                # local platform: http://file-sharing.localhost
```

Open the page, paste the upload token, pick a file. Files of 16 MiB and
more go up as multipart uploads (one signed URL per part). The DevTools
network tab shows the whole contract: a CORS preflight (`OPTIONS`) against
the bucket endpoint, the signed `PUT` with the declared `Content-Type`,
the `ETag` the bucket exposes, and the signed `GET` a share link redirects
to.

`skali deploy` runs the same thing on a cluster. The bucket declares a
route (`STORAGE_DOMAIN`), so `{{ buckets.files.endpoint }}` is that
hostname's https origin and signed URLs work for any browser; a bucket
without a route is only reachable inside the cluster. The application's
own traffic (verification, listing, deletes) uses
`{{ buckets.files.internal_endpoint }}`, the in-cluster gateway, so it
never hairpins through the edge.

## The API

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `POST` | `/api/uploads` | token | Record an upload; returns a signed `PUT` URL (or one per part when `parts > 1`) |
| `POST` | `/api/uploads/{id}/complete` | token | Verify the object landed (assemble a multipart upload) and publish it |
| `DELETE` | `/api/uploads/{id}` | token | Abort a pending upload and free its parts |
| `GET` | `/api/uploads` | token | Pending records and the multipart uploads the bucket still holds open |
| `GET` | `/api/files` | | Published files |
| `GET` | `/api/files/{id}` | | One file with a signed download URL |
| `GET` | `/files/{id}` | | Share link: redirects to a fresh signed download |
| `DELETE` | `/api/files/{id}` | token | Delete a file and its object |

Signing details worth copying: path-style addressing, region `us-east-1`,
the published endpoint as the signing host (never rewritten afterwards),
the `Content-Type` of single uploads signed in, short expiries
(`PRESIGN_TTL`, 15 minutes by default; `expiresIn` per request), and a
server-side `HEAD` before an upload counts.

## Environment

`${NAME}` is required, `${NAME:-default}` optional:

- `UPLOAD_TOKEN` (required): the bearer token that authorizes signing.
- `PRESIGN_TTL` (optional): signed URL lifetime, a Go duration up to 24h.
- `STORAGE_DOMAIN` (required): the bucket's public hostname (the `route`
  of the `files` bucket); on a cluster, point its DNS at the installation.
- `S3_PUBLIC_ENDPOINT` (optional): overrides the host URLs are signed
  for. Local development maps the store to a loopback port
  (`127.0.0.1:30510` unless `SKALI_DEV_LOOPBACK_PORT_BASE` shifts the
  range); set it to `http://127.0.0.1:30510` to try the browser flow
  against a local cluster without the route's hostname.

## Tests

`cmd/skali/dev_e2e_presigned_test.go` (`task test:dev`) deploys this example and
replays the browser's requests in Go: preflight, signed upload, download,
range read, multipart, and the ways a URL must fail (tampered signature,
other method, other key, other host, expired, unsigned, other bucket).
