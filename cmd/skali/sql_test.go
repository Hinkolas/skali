package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func readScript(t *testing.T, script io.ReadCloser) string {
	t.Helper()
	defer script.Close()
	data, err := io.ReadAll(script)
	require.NoError(t, err)
	return string(data)
}

func TestSQLInput(t *testing.T) {
	// -c is the script, whatever the terminals.
	script, interactive, err := sqlInput(sqlInvocation{command: "SELECT 1"}, true, true)
	require.NoError(t, err)
	require.False(t, interactive)
	require.Equal(t, "SELECT 1", readScript(t, script))

	path := filepath.Join(t.TempDir(), "seed.sql")
	require.NoError(t, os.WriteFile(path, []byte("INSERT INTO t VALUES (1);"), 0o600))
	script, interactive, err = sqlInput(sqlInvocation{file: path, format: "json"}, true, true)
	require.NoError(t, err)
	require.False(t, interactive)
	require.Equal(t, "INSERT INTO t VALUES (1);", readScript(t, script))

	_, _, err = sqlInput(sqlInvocation{file: filepath.Join(t.TempDir(), "missing.sql")}, false, false)
	require.ErrorIs(t, err, os.ErrNotExist, "a missing file fails before any session opens")

	// Neither: a terminal on both ends is interactive psql, anything else
	// reads the script from stdin.
	script, interactive, err = sqlInput(sqlInvocation{}, true, true)
	require.NoError(t, err)
	require.True(t, interactive)
	script.Close()
	script, interactive, err = sqlInput(sqlInvocation{format: "csv"}, false, true)
	require.NoError(t, err)
	require.False(t, interactive)
	script.Close()

	for _, tc := range []struct {
		inv  sqlInvocation
		want string
	}{
		{sqlInvocation{command: "SELECT 1", file: "x.sql"}, "exclude each other"},
		{sqlInvocation{format: "yaml", command: "SELECT 1"}, `--format "yaml"`},
		{sqlInvocation{format: "json"}, "--format applies to scripts"},
	} {
		_, _, err := sqlInput(tc.inv, true, true)
		require.ErrorContains(t, err, tc.want)
	}
}

func TestSQLDatabaseArg(t *testing.T) {
	command := &cobra.Command{}
	database, err := sqlDatabaseArg(command, []string{"data"})
	require.NoError(t, err)
	require.Equal(t, "data", database)

	dir := t.TempDir()
	t.Chdir(dir)
	_, err = sqlDatabaseArg(command, nil)
	require.ErrorContains(t, err, "no database named")

	manifest := "name: shop\ndatabases:\n  data:\n    engine: postgres\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skali.yml"), []byte(manifest), 0o600))
	database, err = sqlDatabaseArg(command, nil)
	require.NoError(t, err)
	require.Equal(t, "data", database, "the sole database is the default")

	manifest += "  events:\n    engine: postgres\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skali.yml"), []byte(manifest), 0o600))
	_, err = sqlDatabaseArg(command, nil)
	require.ErrorContains(t, err, "name one of: data, events")
}

// blockingOpen stands in for a session that never ends on its own.
func blockingOpen(ctx context.Context) (*client.ExecSession, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func noReauth(context.Context) error { return errors.New("no reauth expected") }

func TestRunSessionTimeout(t *testing.T) {
	err := runSession(context.Background(), sessionDrive{timeout: 50 * time.Millisecond}, blockingOpen, noReauth)
	coded, ok := errors.AsType[*codedError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 124, coded.code)
	require.Equal(t, 124, exitCodeFor(err))
	require.EqualError(t, err, "session timed out after 50ms")
}

func TestRunSessionRemoteStatusWins(t *testing.T) {
	// A session that ended with the remote's own status before the
	// deadline fired keeps it.
	ctx, cancel := context.WithCancel(context.Background())
	open := func(context.Context) (*client.ExecSession, error) {
		cancel()
		return nil, &client.ExecExitError{Code: 3}
	}
	err := runSession(ctx, sessionDrive{}, open, noReauth)
	exit, ok := errors.AsType[*client.ExecExitError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 3, exit.Code)
}

func TestSessionReauthNeverReadsPipedStdin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/auth/session", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":{"email":"nick@example.com","two_factor_enabled":false},"session":{}}`))
	}))
	t.Cleanup(srv.Close)

	// The test's stdin is not a terminal: the script stays untouched.
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	_, err = writer.WriteString("SELECT 1;\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	original := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() { os.Stdin = original; _ = reader.Close() })

	command := &cobra.Command{}
	api := client.New(srv.URL, "tok", client.Caller{})
	err = sessionReauth(command, api, "prod")(context.Background())
	require.ErrorContains(t, err, "skali does not prompt while stdin carries the session's input")
	require.ErrorContains(t, err, "skali remote login prod")

	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "SELECT 1;\n", string(rest), "reauth must not consume the session's input")
}
