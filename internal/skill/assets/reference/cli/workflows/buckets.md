# Buckets

Read with `skali skill read cli workflows buckets`.
[CLI overview and command index](../../cli.md).

- `skali bucket rotate <key> [--environment <name>] [--retire-after 1h]`
  issues a new S3 keypair for the bucket declared as `buckets.<key>`
  (environment maintain, recent login). The store accepts both keypairs,
  the applications referencing the bucket restart with the new one (the
  run streams like a deploy and succeeds once they all run with it), and
  after the window the previous keypair is retired: requests and presigned
  URLs signed with it fail from then on, so the window must cover the
  longest URL the application issues (`1m` at least for a leaked key, `7d`
  at most). Processes holding the old keypair outside the cluster (`skali
  dev` host runs, a revealed copy) must fetch it again. Rotating again
  inside the window retires the older keypair at once; a bucket a restore
  is rewriting cannot be rotated until the restore finishes.
