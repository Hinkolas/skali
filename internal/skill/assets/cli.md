# Working with the skali CLI

The CLI is self describing: run `skali --help` and `skali <command> --help`
for the current surface.

Two commands work without any cluster and are useful while editing a
manifest:

- `skali validate` parses and validates the manifest, reporting errors
  with file, line, and column.
- `skali compile` prints the compiled project definition, showing
  defaults and normalized units.

Deeper guidance on driving skali from a coding agent (local dev clusters
with `skali dev`, deploys, logs, runs) ships in a later release of this
skill.
