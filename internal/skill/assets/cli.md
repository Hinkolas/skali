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
  environment, and the cluster's routes reach the host process. Ctrl-C
  pauses the project; `d` detaches (host dev processes still stop).
- `skali dev --preview` deploys everything in the cluster, exactly like a
  remote deploy would, ignoring dev blocks.
- `skali dev run <name>` runs a named command from the manifest
  (`applications.<app>.commands`) on this machine with the application's
  resolved environment; `skali dev run <app> -- <command>...` runs a raw
  command. Database and bucket addresses are rewritten to the local
  platform's loopback ports, so seeds and migrations just work.
- `skali dev exec [app]` opens a shell inside the running container
  instead; `skali run` manages journal runs, not project commands.
