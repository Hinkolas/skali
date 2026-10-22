package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/utils"
)

// sqlInvocation carries the flag surface shared by `skali sql` and `skali
// dev sql`.
type sqlInvocation struct {
	command          string
	file             string
	format           string
	readOnly         bool
	statementTimeout time.Duration
	timeout          time.Duration
}

const sqlLong = "Sql runs SQL against one database of the project, declared as\n" +
	"databases.<database> in skali.yml, connected with that database's own\n" +
	"credentials. Nothing needs to be installed locally and no application\n" +
	"has to run: the platform starts a short-lived database client next to\n" +
	"the environment's applications for the session (a few seconds, longer\n" +
	"while its image is first pulled).\n\n" +
	"In a terminal, without -c or -f, it opens an interactive psql. Otherwise\n" +
	"it runs a script: the -c text, the -f file (- for stdin), or stdin\n" +
	"itself. Results go to stdout, errors and notices to stderr, and the\n" +
	"exit status follows psql: 0 success, 1 a client failure, 2 the\n" +
	"database unreachable, 3 the script failed (a script stops at its\n" +
	"first error).\n\n" +
	"--format psql (the default) is psql's own output and understands psql\n" +
	"meta-commands such as \\d. --format csv prints psql's CSV without command\n" +
	"tags. --format json prints one document for the whole script:\n\n" +
	"  {\"results\": [{\"columns\": [{\"name\": \"id\", \"type\": \"int4\", \"type_oid\": 23}],\n" +
	"                \"rows\": [[1]], \"command\": \"SELECT 1\", \"rows_affected\": 1}],\n" +
	"   \"error\": {\"code\": \"42P01\", \"message\": \"...\"}}\n\n" +
	"Rows are arrays aligned with the columns; booleans, numbers and json\n" +
	"keep their JSON types, everything else is PostgreSQL's text form. The\n" +
	"json format runs the script as one query, so its statements share one\n" +
	"implicit transaction unless the script manages its own, and psql\n" +
	"meta-commands are not available there.\n\n" +
	"--read-only sets default_transaction_read_only, a guard against\n" +
	"accidental writes rather than a permission. The session needs maintain\n" +
	"on the environment, like exec, and a recent login on a promote-only one."

func addSQLFlags(command *cobra.Command, inv *sqlInvocation) {
	command.Flags().StringVarP(&inv.command, "command", "c", "",
		"run this SQL and exit (several statements may be separated by semicolons)")
	command.Flags().StringVarP(&inv.file, "file", "f", "",
		"run the SQL script in this file and exit; - reads it from stdin")
	command.Flags().StringVar(&inv.format, "format", "",
		"output of a script: psql (default), csv, or json")
	command.Flags().BoolVar(&inv.readOnly, "read-only", false,
		"start every transaction read-only (a guard, not a permission)")
	command.Flags().DurationVar(&inv.statementTimeout, "statement-timeout", 0,
		"cancel any statement running longer than this, such as 30s (default: the server's)")
	addSessionTimeoutFlag(command, &inv.timeout)
	_ = command.RegisterFlagCompletionFunc("format", cobra.FixedCompletions(
		[]cobra.Completion{"psql", "csv", "json"}, cobra.ShellCompDirectiveNoFileComp))
}

func newSQLCommand() *cobra.Command {
	var environment, remote string
	var inv sqlInvocation
	command := &cobra.Command{
		Use:               "sql [database] [flags]",
		Short:             "Run SQL against a database",
		Long:              sqlLong,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeDatabaseArg,
		RunE: func(command *cobra.Command, args []string) error {
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(command.Context(), start, environment, remote)
			if err != nil {
				return err
			}
			database, err := sqlDatabaseArg(command, args)
			if err != nil {
				return err
			}
			reauth := sessionReauth(command, target.api, target.remoteName)
			return runSQLSession(command, target.api, target.environmentID, database, inv, reauth)
		},
	}
	command.Flags().StringVar(&environment, "environment", "",
		"environment name (default: the checkout binding)")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	addSQLFlags(command, &inv)
	return command
}

func newDevSQLCommand() *cobra.Command {
	var inv sqlInvocation
	command := &cobra.Command{
		Use:   "sql [database] [flags]",
		Short: "Run SQL against a database of the local platform",
		Long: strings.Replace(sqlLong, "against one database of the project",
			"against one database of the project on the local platform", 1),
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeDatabaseArg,
		RunE: func(command *cobra.Command, args []string) error {
			database, err := sqlDatabaseArg(command, args)
			if err != nil {
				return err
			}
			api, environmentID, err := localProjectEnvironment(command)
			if err != nil {
				return err
			}
			reauth := func(ctx context.Context) error { return reauthLocal(ctx, api) }
			err = runSQLSession(command, api, environmentID, database, inv, reauth)
			if client.IsNotFound(err) {
				// The claim appears with the first deploy of the database.
				return fmt.Errorf("%w; has a dev session deployed it? start one with skali dev", err)
			}
			return err
		},
	}
	addSQLFlags(command, &inv)
	return command
}

// sqlDatabaseArg picks the database: the one named, the manifest's sole
// database, or an interactive choice among several.
func sqlDatabaseArg(command *cobra.Command, args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	keys := utils.SortedKeys(manifestDatabases())
	switch len(keys) {
	case 0:
		return "", errors.New("no database named and no manifest declaring one found; name it: skali sql <database>")
	case 1:
		return keys[0], nil
	}
	if !cliprompt.Interactive() {
		return "", fmt.Errorf("several databases in the manifest; name one of: %s", strings.Join(keys, ", "))
	}
	session := promptSession(command.ErrOrStderr(), bufio.NewReader(command.InOrStdin()))
	options := make([]cliprompt.Option, 0, len(keys))
	for _, key := range keys {
		options = append(options, cliprompt.Option{Label: key, Value: key})
	}
	return session.Select(command.Context(), cliprompt.SelectOptions{Title: "Database", Options: options})
}

// sqlInput resolves where the session's input comes from: the script
// (closed by the caller) and whether the session is interactive psql.
func sqlInput(inv sqlInvocation, stdinTerminal, stdoutTerminal bool) (io.ReadCloser, bool, error) {
	switch inv.format {
	case "", "psql", "csv", "json":
	default:
		return nil, false, fmt.Errorf("--format %q: want psql, csv, or json", inv.format)
	}
	if inv.command != "" && inv.file != "" {
		return nil, false, errors.New("-c and -f exclude each other")
	}
	switch {
	case inv.command != "":
		return io.NopCloser(strings.NewReader(inv.command)), false, nil
	case inv.file == "-":
		return io.NopCloser(os.Stdin), false, nil
	case inv.file != "":
		file, err := os.Open(inv.file)
		if err != nil {
			return nil, false, err
		}
		return file, false, nil
	}
	if stdinTerminal && stdoutTerminal {
		if inv.format != "" && inv.format != "psql" {
			return nil, false, errors.New("--format applies to scripts: pass -c or -f, or pipe the script on stdin")
		}
		return io.NopCloser(os.Stdin), true, nil
	}
	return io.NopCloser(os.Stdin), false, nil
}

// runSQLSession opens the SQL session and drives it with runSession. A
// session on a terminal says what it is waiting for: the client takes a
// few seconds to start.
func runSQLSession(command *cobra.Command, api *client.Client, environmentID, database string,
	inv sqlInvocation, reauth func(context.Context) error) error {
	script, interactive, err := sqlInput(inv, clirender.IsTerminal(os.Stdin), clirender.IsTerminal(os.Stdout))
	if err != nil {
		return err
	}
	defer script.Close()
	if inv.statementTimeout < 0 {
		return errors.New("--statement-timeout must not be negative")
	}
	opts := client.SQLOptions{
		Format:           inv.format,
		TTY:              interactive,
		ReadOnly:         inv.readOnly,
		StatementTimeout: inv.statementTimeout,
	}
	if stderr, ok := command.ErrOrStderr().(*os.File); ok && clirender.IsTerminal(stderr) {
		style := clirender.StyleFor(stderr)
		fmt.Fprintln(stderr, style.Dim("starting a database client for databases."+database+"…"))
	}
	return runSession(command.Context(), sessionDrive{tty: interactive, stdin: script, timeout: inv.timeout},
		func(ctx context.Context) (*client.ExecSession, error) {
			return api.SQL(ctx, environmentID, database, opts)
		}, reauth)
}
