# file-sharing (reference manifest)

This example is a reference for the manifest surface, not a runnable
project: `skali.yml` declares an application with resources, autoscaling,
placement, rollout settings, a project-isolated Postgres database, a bucket,
and a backup schedule, but no application source ships with it, so
`skali dev` and `skali deploy` stop at the build step.

Use it to look up field shapes (`skali validate` and `skali compile` work
on it). For examples that build and run, see
[`hello-world`](../hello-world), [`whoami`](../whoami),
[`guestbook`](../guestbook), and [`dev-loop`](../dev-loop).
