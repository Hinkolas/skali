# Databases, SQL, and container exec

Read with `skali skill read cli workflows databases`.
[CLI overview and command index](../../cli.md).

- `skali database rotate <key> [--environment <name>] [--retire-after 1h]`
  issues a new login role for the database declared as `databases.<key>`
  (environment maintain, recent login). The pool accepts both roles, the
  applications referencing the database restart with the new username and
  password (the run streams like a deploy and succeeds once they all run
  with them), and after the window the previous login role is retired: its
  open sessions are terminated and the role is dropped (`1m` at least for
  a leaked password, `7d` at most). The database and everything in it stay
  owned by the owner role the login roles act as, so migrations and
  restores are unaffected; the username changes with every rotation.
  Processes holding the old credentials outside the cluster (`skali dev`
  host runs, a revealed copy) must fetch them again. Rotating again inside
  the window retires the older login role at once.
- `skali database list`, `show <pool>` and `set <pool>` inspect and tune
  the database pools (admin only). The connections column is the pool's
  connection check: `refusing` means new application logins fail for lack
  of connection slots, and `show` names the open incident and which
  databases hold the connections.
  `set --memory` uses Kubernetes quantities such as `4Gi` or `512Mi` and
  cannot be combined with `--auto-memory`. `--set key=value` and `--unset key`
  are repeatable; changes can restart pool instances. These are pool names,
  whereas `database rotate` and `sql` take manifest database keys.
- `skali sql [database] [--environment <name>]` runs SQL against
  `databases.<database>` (the manifest's sole database when omitted) with
  that database's own credentials, in a short-lived client the platform
  starts for the session (a few seconds); no application has to run and
  nothing is installed locally. In a terminal without `-c` or `-f` it is
  an interactive psql. `-c '<sql>'`, `-f <file>` (`-` for stdin) or piped
  stdin run a script: results on stdout, errors and notices on stderr,
  and psql's exit statuses in every format (0 success, 1 client failure,
  2 database unreachable, 3 the script failed; it stops at the first
  error). `--format psql` (default) understands meta-commands such as
  `\d <table>`; `--format csv` drops command tags; `--format json` prints
  one document `{"results": [{"columns": [{"name", "type", "type_oid"}],
  "rows": [[...]], "command", "rows_affected"}], "error": {"code",
  "message", ...}}` with rows as arrays aligned with the columns (COPY TO
  STDOUT output as base64 `copy_data`) and runs the script as one query (one
  implicit transaction, no meta-commands, no COPY FROM STDIN).
  `--read-only` guards against accidental writes (not a permission),
  `--statement-timeout 30s` cancels slow statements on the server, and
  `--timeout 5m` ends the whole session with status 124. Needs `maintain`
  on the environment, plus a recent login on a promote-only one.
  `--command/-c` and `--file/-f` are mutually exclusive; `--format` requires
  a script via one of them or piped stdin, rather than an interactive session.
- `skali exec [app] -- <command>...` runs a command in a running
  application container (an interactive shell without one). Without a
  terminal it pipes stdin, stdout and stderr and exits with the remote
  status; `--no-stdin` sends no input, `--timeout` ends the session with
  status 124, and an interrupted session exits 130. Neither exec nor sql
  ever reads a piped stdin to confirm a login: when a promote-only
  environment needs a recent login, confirm in a terminal first
  (`skali remote login`).
  `--pod` selects a specific pod, `--container` a container in it. TTY allocation
  is automatic when stdin/stdout are terminals; `--tty` forces it, `--no-tty`
  disables it, and `--no-stdin` implies no TTY and cannot be combined with
  `--tty`. Put command flags after `--` so Skali does not parse them.
