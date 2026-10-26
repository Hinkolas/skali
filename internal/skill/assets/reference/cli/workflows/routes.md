# Routes

Read with `skali skill read cli workflows routes`.
[CLI overview and command index](../../cli.md).

- `skali route list` shows each route of the environment, and each of its
  aliases on a line of its own, with its URL,
  certificate state, and the reconciler's last verdict on whether the
  domain reaches this installation (`cert deferred · domain not pointing
  here yet` while DNS is elsewhere). `skali route probe` checks the domains
  right now and prints one line per resolved address (answered by this
  installation, by another server, or not at all); a domain that answers
  here counts as arrived and the reconciler requests a fresh certificate
  issuance without waiting for its cadence or cert-manager's backoff. Both
  take `--environment <name>` and `--remote <name>`; probe needs `deploy`
  on the environment.
