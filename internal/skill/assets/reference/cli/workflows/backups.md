# Backups

Read with `skali skill read cli workflows backups`.
[CLI overview and command index](../../cli.md).

- `skali backup target set --endpoint <url> --bucket <name> --access-key <id>`
  (admin) configures the external S3 location every snapshot goes to;
  `show` and `unset` read and remove it. Nothing is backed up without it.
- `skali backup create [--environment <name>]` takes a manual snapshot of
  every database, bucket, and volume of the environment; the run streams
  like a deploy. Manual snapshots are kept until removed. An environment
  whose manifest declares no database, bucket, or volume has nothing to
  snapshot and is refused (`nothing_to_back_up`); the Studio disables
  the button there.
- `skali backup schedule set --environment <name> --every "<cron>" --keep
  <duration>` turns automatic backups on for one environment (environment
  admin, recent login): skalid snapshots every database, bucket, and volume
  on the five-field cron schedule (UTC) and deletes the snapshots the
  schedule took once they pass the retention, always keeping the newest.
  `skali backup schedule show` prints the setting, `skali backup schedule
  remove` turns it off (existing snapshots stay and stop expiring). The
  runs carry actor `schedule`. Nothing is on until someone sets it.
- `skali backup list` shows every snapshot of the project with its
  environment and origin (`manual` or `scheduled`); `--environment`
  filters. `skali backup restore [<snapshot-id>]` restores into the
  environment the snapshot came from (or `--environment <other>`) after a
  typed confirmation; the snapshot is verified first, and the environment
  stops while data is written. A restore that fails after the stop keeps
  the environment down and refuses deploys (`restore_incomplete`) until a
  restore completes; `skali backup accept [--environment <name>]` accepts
  the data as it is instead, and the next deploy brings the environment
  back. `skali backup remove <snapshot-id>` deletes a snapshot for good.
  Restore, accept, and remove need `maintain` on the environment and a
  recent login. Snapshots whose original environment was purged need
  project admin; restore requires an explicit `--environment` target. Renamed
  environments keep their snapshots and list them under their current name.
