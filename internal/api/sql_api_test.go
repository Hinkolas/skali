package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	kexec "k8s.io/client-go/util/exec"

	"github.com/Hinkolas/skali/internal/execproto"
	"github.com/Hinkolas/skali/internal/podexec"
	"github.com/Hinkolas/skali/internal/sqlsession"
)

// fakeSQLService scripts the SQL session surface per test. Unscripted
// calls fail loudly so unrelated tests cannot exercise the route by
// accident.
type fakeSQLService struct {
	mu      sync.Mutex
	resolve func(ctx context.Context, environmentID uuid.UUID, opts sqlsession.Options) (*sqlsession.Target, error)
	run     func(ctx context.Context, target *sqlsession.Target, streams podexec.Streams) (string, error)
}

func (f *fakeSQLService) script(
	resolve func(ctx context.Context, environmentID uuid.UUID, opts sqlsession.Options) (*sqlsession.Target, error),
	run func(ctx context.Context, target *sqlsession.Target, streams podexec.Streams) (string, error),
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolve = resolve
	f.run = run
}

func (f *fakeSQLService) Resolve(ctx context.Context, environmentID uuid.UUID, opts sqlsession.Options) (*sqlsession.Target, error) {
	f.mu.Lock()
	resolve := f.resolve
	f.mu.Unlock()
	if resolve == nil {
		return nil, errors.New("fakeSQLService: Resolve not scripted")
	}
	return resolve(ctx, environmentID, opts)
}

func (f *fakeSQLService) Run(ctx context.Context, target *sqlsession.Target, streams podexec.Streams) (string, error) {
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return "", errors.New("fakeSQLService: Run not scripted")
	}
	return run(ctx, target, streams)
}

// resolveAs echoes the options back as a target, the way the real service
// does once the claim checks out.
func resolveAs(t *testing.T, got *sqlsession.Options) func(context.Context, uuid.UUID, sqlsession.Options) (*sqlsession.Target, error) {
	return func(_ context.Context, id uuid.UUID, opts sqlsession.Options) (*sqlsession.Target, error) {
		if got != nil {
			*got = opts
		}
		format, err := sqlsession.ParseFormat(string(opts.Format))
		require.NoError(t, err)
		return &sqlsession.Target{
			EnvironmentID: id, Namespace: "ns", Database: opts.Database,
			Format: format, TTY: opts.TTY, ReadOnly: opts.ReadOnly, StatementTimeout: opts.StatementTimeout,
		}, nil
	}
}

func TestSQLHandshakeErrors(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)
	path := "/v1/environments/" + envID + "/databases/data/sql"

	status, body := a.do("GET", path, "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_token", errorCode(t, body))

	// Resolve failures surface as normal envelopes before any upgrade.
	for _, tc := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{sqlsession.ErrNoCluster, http.StatusServiceUnavailable, "node_unreachable", "no cluster"},
		{sqlsession.ErrEnvironmentNotFound, http.StatusNotFound, "not_found", "not found"},
		{sqlsession.ErrDatabaseNotFound, http.StatusNotFound, "not_found", `no database "data" in this environment`},
		{sqlsession.ErrNotProvisioned, http.StatusConflict, "conflict", "not provisioned"},
		{sqlsession.ErrInvalidOptions, http.StatusBadRequest, "bad_request", "invalid options"},
	} {
		a.sqlFake.script(func(context.Context, uuid.UUID, sqlsession.Options) (*sqlsession.Target, error) {
			return nil, tc.err
		}, nil)
		status, body := a.do("GET", path, token, nil)
		require.Equal(t, tc.status, status, "for %v", tc.err)
		require.Equal(t, tc.code, errorCode(t, body), "for %v", tc.err)
		require.Contains(t, body["error"].(map[string]any)["message"], tc.message, "for %v", tc.err)
	}

	// Malformed session parameters never reach resolution.
	a.sqlFake.script(func(context.Context, uuid.UUID, sqlsession.Options) (*sqlsession.Target, error) {
		t.Fatal("resolve must not run on a malformed request")
		return nil, nil
	}, nil)
	for _, query := range []string{"?tty=maybe", "?read_only=sure", "?statement_timeout=-1", "?statement_timeout=5s"} {
		status, body := a.do("GET", path+query, token, nil)
		require.Equal(t, http.StatusBadRequest, status, query)
		require.Equal(t, "bad_request", errorCode(t, body), query)
	}
}

func TestSQLPromoteOnlyNeedsFreshSession(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)
	path := "/v1/environments/" + envID + "/databases/data/sql"
	a.sqlFake.script(func(context.Context, uuid.UUID, sqlsession.Options) (*sqlsession.Target, error) {
		return nil, sqlsession.ErrNoCluster
	}, nil)

	// A direct environment opens on maintain alone, stale session or not.
	a.staleAllSessions()
	status, body := a.do("GET", path, token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "node_unreachable", errorCode(t, body))

	a.setEnvironmentSettings(t, envID, map[string]any{"deploy_policy": "promote-only"})
	status, body = a.do("GET", path, token, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
	status, _ = a.do("POST", "/v1/auth/reauth", token, map[string]string{"password": "hunter2hunter2"})
	require.Equal(t, http.StatusNoContent, status)
	status, body = a.do("GET", path, token, nil)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "node_unreachable", errorCode(t, body))
}

func TestSQLSessionStreamsScript(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	envID := a.execEnvironment(t, token)

	var gotOpts sqlsession.Options
	started := make(chan struct{})
	a.sqlFake.script(resolveAs(t, &gotOpts),
		func(_ context.Context, target *sqlsession.Target, streams podexec.Streams) (string, error) {
			require.Equal(t, sqlsession.FormatJSON, target.Format)
			// The client takes a moment to start; the script the client
			// sent meanwhile is still there once it reads.
			<-started
			script, err := io.ReadAll(streams.Stdin)
			require.NoError(t, err)
			_, _ = streams.Stdout.Write([]byte(`{"results":[],"script":"` + string(script) + `"}`))
			_, _ = streams.Stderr.Write([]byte("NOTICE:  hi"))
			return "skali-sql-1", nil
		},
	)

	conn, res, err := a.dialExec(token, "/v1/environments/"+envID.String()+
		"/databases/data/sql?format=json&read_only=true&statement_timeout=1500")
	require.NoError(t, err)
	defer conn.Close()
	require.Equal(t, testInstanceID, res.Header.Get(InstanceHeader))

	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage,
		execproto.EncodeData(execproto.ChannelStdin, []byte("SELECT 1"))))
	eof, err := execproto.EncodeControl(execproto.Control{Type: execproto.ControlStdinEOF})
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, eof))
	time.Sleep(50 * time.Millisecond)
	close(started)

	transcript := readTranscript(t, conn)
	require.Equal(t, `{"results":[],"script":"SELECT 1"}`, transcript.stdout.String())
	require.Equal(t, "NOTICE:  hi", transcript.stderr.String())
	require.Equal(t, execproto.ControlExit, transcript.terminal.Type)
	require.Zero(t, transcript.terminal.Code)

	require.Equal(t, "data", gotOpts.Database)
	require.Equal(t, sqlsession.FormatJSON, gotOpts.Format)
	require.True(t, gotOpts.ReadOnly)
	require.False(t, gotOpts.TTY)
	require.Equal(t, 1500*time.Millisecond, gotOpts.StatementTimeout)
}

func TestSQLSessionExitAndStartFailure(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	envID := a.execEnvironment(t, token)
	path := "/v1/environments/" + envID.String() + "/databases/data/sql"

	// A failing script is the client's exit status, passed through.
	a.sqlFake.script(resolveAs(t, nil),
		func(context.Context, *sqlsession.Target, podexec.Streams) (string, error) {
			return "skali-sql-1", kexec.CodeExitError{Err: errors.New("exit 3"), Code: 3}
		})
	conn, _, err := a.dialExec(token, path)
	require.NoError(t, err)
	transcript := readTranscript(t, conn)
	conn.Close()
	require.Equal(t, execproto.ControlExit, transcript.terminal.Type)
	require.Equal(t, 3, transcript.terminal.Code)

	// A client that never starts is its own error code.
	a.sqlFake.script(resolveAs(t, nil),
		func(context.Context, *sqlsession.Target, podexec.Streams) (string, error) {
			return "", &sqlsession.StartError{Err: errors.New("ErrImagePull: not found")}
		})
	conn, _, err = a.dialExec(token, path)
	require.NoError(t, err)
	transcript = readTranscript(t, conn)
	conn.Close()
	require.Equal(t, execproto.ControlError, transcript.terminal.Type)
	require.Equal(t, execproto.ErrCodeStartFailed, transcript.terminal.ErrCode)
	require.True(t, strings.HasPrefix(transcript.terminal.Message, "start the database client: ErrImagePull"),
		transcript.terminal.Message)
}
