# Working with the skali CLI

The CLI is self describing: run `skali --help` and `skali <command> --help`
for the current surface.

Two commands work without any cluster and are useful while editing a
manifest:

- `skali validate` parses and validates the manifest, reporting errors
  with file, line, and column.
- `skali compile` prints the compiled project definition, showing
  defaults and normalized units.

## Local development

- `skali dev` is the paved path: it ensures the disposable local platform
  and deploys the project. Applications with a `dev:` block are not built;
  their dev command runs on this machine with the real resolved
  environment, and the cluster's routes reach the host process on
  auto-allocated ports (injected as `SKALI_PORT_<NAME>` and `PORT`,
  usable as `${PORT}` in the dev command). Ctrl-C pauses the project;
  `d` detaches (host dev processes still stop).
- `skali dev --preview` deploys everything in the cluster, exactly like a
  remote deploy would, ignoring dev blocks.
- `skali dev run <name>` runs a named command from the manifest
  (`applications.<app>.commands`) on this machine with the application's
  resolved environment; `skali dev run <app> -- <command>...` runs a raw
  command. Database and bucket addresses are rewritten to the local
  platform's loopback ports, so seeds and migrations just work.
- `skali dev exec [app]` opens a shell inside the running container
  instead; `skali run` manages journal runs, not project commands.

## Access

- `skali access ls` shows who holds which role on the project: the project
  role per member and the effective role per environment (`*` marks an
  explicit per-environment role). `skali access set <email> <role>` grants
  or changes a project role (read, deploy, maintain, admin); with
  `--environment <name>` it sets an explicit role on that environment only
  (`none` locks it). `skali access rm <email>` removes a member.
- `skali env ls` lists environments with your access, priority, deploy
  policy, and ceiling; `skali env create <name>`, `skali env set
  --environment <name> --max-role read --deploy-policy promote-only`, and
  `skali env rm <name>` (purge) configure them. Writes need a recent login;
  the CLI asks for the password when it has aged.
- A deploy needs `deploy` on the environment for an unchanged definition and
  `maintain` to change the definition or stage values; refusals name the
  required role.

