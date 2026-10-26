# Access

Read with `skali skill read cli workflows access`.
[CLI overview and command index](../../cli.md).

- `skali access list` shows who holds which role on the project: the project
  role per member and the effective role per environment (`*` marks an
  explicit per-environment role). `skali access set <email> <role>` grants
  or changes a project role (read, deploy, maintain, admin); with
  `--environment <name>` it sets an explicit role on that environment only
  (`none` locks it). `skali access remove <email>` removes a member.
- `skali env list` lists environments with their state (active, stopped,
  removing), your access, priority, deploy policy, ceiling, and backup
  schedule; `skali env create <name>`, `skali env set --environment <name>
  --max-role read --deploy-policy promote-only`, `skali env stop <name>` (takes
  the workloads down, keeps the data; the next deploy starts it again), and
  `skali env remove <name>` (purge: deletes the environment with all its data,
  typed confirmation) configure them. Stop and remove follow their run until
  done; `--detach` returns right away. Writes need a recent login; the CLI asks
  for the password when it has aged.
- `skali project remove [project]` removes a project with every environment
  and all their data (project admin, typed confirmation, recent login): each
  environment is purged and the project goes with the last of them. It
  reports each environment as it goes; `--detach` returns right away, and
  repeating the command resumes an interrupted removal.
- `skali env rename <environment> <new-name>` renames without redeploying or
  moving data (environment admin, recent login). Promotion rules follow the new
  name; old checkout bindings remain valid through reserved name aliases until
  the environment is purged. `--project` and `--remote` select another project
  or installation. Studio exposes Rename in environment settings.
- A deploy needs `deploy` on the environment for an unchanged definition and
  `maintain` to change the definition or stage values; refusals name the
  required role. A promote-only environment refuses direct deploys and
  names the way in: `skali deploy --from <source> --environment <name>`
  promotes a source's active revision (sources limited by `--promote-from`).
  Environment admins may force a direct deploy with
  `skali deploy --bypass-protection`; it asks for the password when the
  login has aged and the run records the bypass.
