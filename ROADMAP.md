# Skali roadmap

A rough idea of where skali is heading. There are no dates, and things can
change. Concrete work is tracked in
[issues](https://github.com/Hinkolas/skali/issues), and what already
shipped is in the [release notes](https://github.com/Hinkolas/skali/releases).

## Operations

- **Cluster observability:** give admins better observability over their
  cluster, not just their projects
- **Alerting:** get notified when something breaks, like a node going down
  or a backup failing
- **Backups:** improve the backup system, for example with point-in-time
  restores for databases and backups of skali's own state
- **Cluster management:** make day-to-day cluster work easier, like
  changing settings, updating, and swapping out nodes

## Studio

- **Dashboard:** add a cluster dashboard as the starting page
- **Logs and activity:** show logs and project activity in the Studio
- **Service graph:** see a project's services and how they connect to each
  other at a glance
- **Web terminal:** run commands in an application from the browser, like
  `skali exec` does in the CLI
- **Database client:** browse tables, look at raw data, and run SQL right
  in the Studio
- **Bucket browser:** look through a bucket's contents, upload, and
  download files

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
  work today
- **Preview environments:** one environment per branch
- **Autoscaling:** scale applications with their load
