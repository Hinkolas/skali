# skali dev sql

<!-- GENERATED COMMAND REFERENCE: edit command help and run go generate ./cmd/skali. -->

Read with `skali skill read cli dev sql`.
[CLI overview](../../cli.md). [Workflow guide](../workflows/databases.md): `skali skill read cli workflows databases`.

```text
skali dev sql [database] [flags]
```

Sql runs SQL against one database of the project on the local platform, declared as
databases.<database> in skali.yml, connected with that database's own
credentials. Nothing needs to be installed locally and no application
has to run: the platform starts a short-lived database client next to
the environment's applications for the session (a few seconds, longer
while its image is first pulled).

In a terminal, without -c or -f, it opens an interactive psql. Otherwise
it runs a script: the -c text, the -f file (- for stdin), or stdin
itself. Results go to stdout, errors and notices to stderr, and the
exit status follows psql: 0 success, 1 a client failure, 2 the
database unreachable, 3 the script failed (a script stops at its
first error).

--format psql (the default) is psql's own output and understands psql
meta-commands such as \d. --format csv prints psql's CSV without command
tags. --format json prints one document for the whole script:

  {"results": [{"columns": [{"name": "id", "type": "int4", "type_oid": 23}],
                "rows": [[1]], "command": "SELECT 1", "rows_affected": 1}],
   "error": {"code": "42P01", "message": "..."}}

Rows are arrays aligned with the columns; booleans, numbers and json
keep their JSON types, everything else is PostgreSQL's text form; the
output of COPY ... TO STDOUT is kept byte for byte as base64 in
"copy_data" ("copy_encoding": "base64"). The json
format runs the script as one query, so its statements share one
implicit transaction unless the script manages its own; psql
meta-commands and COPY ... FROM STDIN are not available there.

--read-only sets default_transaction_read_only, a guard against
accidental writes rather than a permission. The session needs maintain
on the environment, like exec, and a recent login on a promote-only one.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--command`, `-c` | `string` | `""` | run this SQL and exit (several statements may be separated by semicolons) |
| `--file`, `-f` | `string` | `""` | run the SQL script in this file and exit; - reads it from stdin |
| `--format` | `string` | `""` | output of a script: psql (default), csv, or json |
| `--help`, `-h` | `bool` | `false` | help for sql |
| `--offline` | `bool` | `false` | use the recorded target release without contacting the remote or downloading a CLI (inherited) |
| `--read-only` | `bool` | `false` | start every transaction read-only (a guard, not a permission) |
| `--remote` | `string` | `""` | run the local platform at the release this remote's cluster runs, ignoring the checkout binding and the current remote (inherited) |
| `--skalid-image` | `string` | `""` | control-plane image for the local platform (defaults to the recorded image, the published image of a released skali, or a working-tree build) (inherited) |
| `--statement-timeout` | `duration` | `0s` | cancel any statement running longer than this, such as 30s (default: the server's) |
| `--timeout` | `duration` | `0s` | end the session after this long, such as 30s or 5m, and exit with status 124 (default: no limit) |
| `--verbose` | `bool` | `false` | show every recorded detail under live steps (TLS issuance fields, health snapshots) and dev proxy relay diagnostics (inherited) |
