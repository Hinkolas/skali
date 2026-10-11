# Skali roadmap

A rough idea of where skali is heading. There are no dates, and things can
change. Concrete work is tracked in
[issues](https://github.com/Hinkolas/skali/issues), and what already
shipped is in the [release notes](https://github.com/Hinkolas/skali/releases).

## Operations

- **Cluster observability:** give admins better observability over their
  cluster, not just their projects. The
  [platform health proposal](docs/design/platform-health.md) records the
  broader design; concrete outage detection is tracked separately
- **Alerting:** get notified when something breaks, like a node going down
  or a backup failing
- **Backups:** improve the backup system, for example with point-in-time
  restores for databases, backups of skali's own state, and continuous
  off-cluster protection for bucket data
- **Local data workflows:** restore a remote backup into `skali dev` for
  debugging and realistic test data, with an explicit import and safety
  contract before implementation
- **Resource policy:** define default requests and limits, environment
  caps, priority-aware database placement, and usage visibility
- **Cluster management:** make day-to-day cluster work easier, like
  changing settings, updating, and swapping out nodes

## Studio

- **Dashboard:** add a cluster dashboard as the starting page
- **Logs and activity:** show logs and project activity in the Studio
- **Environment overview:** show health, the active revision, pending
  changes, resolved service values, and scaling controls
- **Service graph:** see a project's services and how they connect to each
  other at a glance
- **Web terminal:** run commands in an application from the browser, like
  `skali exec` does in the CLI
- **Database client:** browse tables, look at raw data, and run SQL right
  in the Studio
- **Bucket browser:** look through a bucket's contents, upload, and
  download files
- **Service visibility:** database and bucket metrics and access details

## Features

- **Push-to-deploy:** deploy on git push, without needing the CLI on a
  laptop
- **Tokens:** API and CI tokens
- **Jobs:** scheduled jobs and one-off commands for applications
- **Database engines:** more engines, starting with Valkey
- **Static sites:** a dedicated service type for static files next to
  applications, databases, and buckets, with options made for serving
  them
- **Add-ons:** install shared services like email or image processing once
  per cluster and attach them to projects, the way databases and buckets
  work today. The [fleet and add-ons proposal](docs/design/fleet-and-addons.md)
  also explores queued work on external machines and managed builds
- **Preview environments:** one environment per branch
- **Autoscaling:** scale applications with their load

## Tracking work

The roadmap describes directions, not commitments. Open issues should name
an observed problem or a bounded deliverable with a verifiable outcome.
Create issues for individual Studio pages and other roadmap features when
their first implementation scope is chosen, rather than keeping umbrella
checklists for every possible feature.

`status: planned` means selected for upcoming work; it is not a synonym for
useful. `status: deferred` means intentionally postponed. An issue without
either status remains in the unscheduled backlog. Priority describes impact,
independently of scheduling.

Earlier discussions remain available in closed issues:
[local backup imports](https://github.com/Hinkolas/skali/issues/31),
[fleet and add-ons](https://github.com/Hinkolas/skali/issues/93),
[platform health](https://github.com/Hinkolas/skali/issues/108),
[resource policy](https://github.com/Hinkolas/skali/issues/117),
[Studio pages](https://github.com/Hinkolas/skali/issues/124), and
[the dashboard](https://github.com/Hinkolas/skali/issues/125). Closing these
trackers does not mean the features have shipped or been rejected.
