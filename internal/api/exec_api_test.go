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
)

// fakeExecService scripts the exec surface per test. Unscripted calls fail
// loudly so unrelated tests cannot exercise the route by accident.
type fakeExecService struct {
	mu      sync.Mutex
	resolve func(ctx context.Context, environmentID uuid.UUID, opts podexec.Options) (*podexec.Session, error)
	stream  func(ctx context.Context, session *podexec.Session, streams podexec.Streams) error
}

func (f *fakeExecService) script(
	resolve func(ctx context.Context, environmentID uuid.UUID, opts podexec.Options) (*podexec.Session, error),
	stream func(ctx context.Context, session *podexec.Session, streams podexec.Streams) error,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolve = resolve
	f.stream = stream
}

func (f *fakeExecService) Resolve(ctx context.Context, environmentID uuid.UUID, opts podexec.Options) (*podexec.Session, error) {
	f.mu.Lock()
	resolve := f.resolve
	f.mu.Unlock()
	if resolve == nil {
		return nil, errors.New("fakeExecService: Resolve not scripted")
	}
	return resolve(ctx, environmentID, opts)
}

func (f *fakeExecService) Stream(ctx context.Context, session *podexec.Session, streams podexec.Streams) error {
	f.mu.Lock()
	stream := f.stream
	f.mu.Unlock()
	if stream == nil {
		return errors.New("fakeExecService: Stream not scripted")
	}
	return stream(ctx, session, streams)
}

// dialExec opens the exec WebSocket with a bearer token, returning the
// handshake response for error assertions.
func (a *testAPI) dialExec(token, path string) (*websocket.Conn, *http.Response, error) {
	a.t.Helper()
	wsURL := "ws" + strings.TrimPrefix(a.srv.URL, "http") + path
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	return websocket.DefaultDialer.Dial(wsURL, header)
}

// execTranscript drains a session until its terminal control frame.
type execTranscript struct {
	stdout   strings.Builder
	stderr   strings.Builder
	terminal execproto.Control
}

func readTranscript(t *testing.T, conn *websocket.Conn) execTranscript {
	t.Helper()
	var transcript execTranscript
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		_, message, err := conn.ReadMessage()
		require.NoError(t, err, "connection closed before a terminal frame")
		channel, payload, err := execproto.Decode(message)
		require.NoError(t, err)
		switch channel {
		case execproto.ChannelStdout:
			transcript.stdout.Write(payload)
		case execproto.ChannelStderr:
			transcript.stderr.Write(payload)
		case execproto.ChannelControl:
			control, err := execproto.ParseControl(payload)
			require.NoError(t, err)
			transcript.terminal = control
			return transcript
		}
	}
}

// execEnvironment creates a project and environment and returns the
// environment id: the scope middleware answers 404 for ids it cannot load
// before the exec handler ever runs.
func (a *testAPI) execEnvironment(t *testing.T, token string) uuid.UUID {
	t.Helper()
	_, envID := a.createEnvironment(t, token)
	id, err := uuid.Parse(envID)
	require.NoError(t, err)
	return id
}

func TestExecHandshakeErrors(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	_, envID := a.createEnvironment(t, token)
	path := "/v1/environments/" + envID + "/exec?service=web"

	// No token.
	status, body := a.do("GET", path, "", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, "invalid_token", errorCode(t, body))

	// Stale session: exec sits behind the reauth gate.
	a.staleAllSessions()
	status, body = a.do("GET", path, token, nil)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "reauth_required", errorCode(t, body))
	status, _ = a.do("POST", "/v1/auth/reauth", token, map[string]string{"password": "hunter2hunter2"})
	require.Equal(t, http.StatusNoContent, status)

	// Resolve failures surface as normal envelopes before any upgrade.
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{podexec.ErrNoCluster, http.StatusServiceUnavailable, "node_unreachable"},
		{podexec.ErrEnvironmentNotFound, http.StatusNotFound, "not_found"},
		{podexec.ErrPodNotFound, http.StatusNotFound, "not_found"},
		{podexec.ErrInvalidOptions, http.StatusBadRequest, "bad_request"},
		{&podexec.NoReadyPodError{Service: "web"}, http.StatusConflict, "no_ready_pod"},
	} {
		a.execFake.script(func(context.Context, uuid.UUID, podexec.Options) (*podexec.Session, error) {
			return nil, tc.err
		}, nil)
		status, body := a.do("GET", path, token, nil)
		require.Equal(t, tc.status, status, "for %v", tc.err)
		require.Equal(t, tc.code, errorCode(t, body), "for %v", tc.err)
	}

	// Malformed tty value.
	a.execFake.script(func(context.Context, uuid.UUID, podexec.Options) (*podexec.Session, error) {
		t.Fatal("resolve must not run on a malformed request")
		return nil, nil
	}, nil)
	status, body = a.do("GET", path+"&tty=maybe", token, nil)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad_request", errorCode(t, body))
}

func TestExecEchoSession(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	envID := a.execEnvironment(t, token)

	var gotOpts podexec.Options
	a.execFake.script(
		func(_ context.Context, id uuid.UUID, opts podexec.Options) (*podexec.Session, error) {
			require.Equal(t, envID, id)
			gotOpts = opts
			return &podexec.Session{
				Namespace: "skali-demo-production", Pod: "web-1", Container: "web",
				Command: opts.Command, TTY: opts.TTY,
			}, nil
		},
		func(_ context.Context, _ *podexec.Session, streams podexec.Streams) error {
			// Echo stdin to stdout until EOF, then note EOF on stderr:
			// exercises channel separation and stdin half-close.
			data, err := io.ReadAll(streams.Stdin)
			require.NoError(t, err)
			_, _ = streams.Stdout.Write(data)
			_, _ = streams.Stderr.Write([]byte("saw eof"))
			return nil
		},
	)

	conn, res, err := a.dialExec(token,
		"/v1/environments/"+envID.String()+"/exec?service=web&cmd=cat&cmd=-")
	require.NoError(t, err)
	defer conn.Close()
	require.Equal(t, testInstanceID, res.Header.Get(InstanceHeader),
		"instance pinning must cover the 101 handshake response")

	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage,
		execproto.EncodeData(execproto.ChannelStdin, []byte("hello exec"))))
	eof, err := execproto.EncodeControl(execproto.Control{Type: execproto.ControlStdinEOF})
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, eof))

	transcript := readTranscript(t, conn)
	require.Equal(t, "hello exec", transcript.stdout.String())
	require.Equal(t, "saw eof", transcript.stderr.String())
	require.Equal(t, execproto.ControlExit, transcript.terminal.Type)
	require.Zero(t, transcript.terminal.Code)

	require.Equal(t, "web", gotOpts.Service)
	require.Equal(t, []string{"cat", "-"}, gotOpts.Command, "cmd params keep their order")
	require.False(t, gotOpts.TTY)
}

func TestExecExitCode(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	envID := a.execEnvironment(t, token)

	session := &podexec.Session{Namespace: "ns", Pod: "web-1", Container: "web", Command: []string{"false"}}
	a.execFake.script(
		func(context.Context, uuid.UUID, podexec.Options) (*podexec.Session, error) { return session, nil },
		func(context.Context, *podexec.Session, podexec.Streams) error {
			return kexec.CodeExitError{Err: errors.New("command terminated with exit code 7"), Code: 7}
		},
	)

	conn, _, err := a.dialExec(token, "/v1/environments/"+envID.String()+"/exec?service=web&cmd=false")
	require.NoError(t, err)
	defer conn.Close()

	transcript := readTranscript(t, conn)
	require.Equal(t, execproto.ControlExit, transcript.terminal.Type)
	require.Equal(t, 7, transcript.terminal.Code)
}

func TestExecInfrastructureError(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	envID := a.execEnvironment(t, token)

	session := &podexec.Session{Namespace: "ns", Pod: "web-1", Container: "web", Command: []string{"sh"}}
	a.execFake.script(
		func(context.Context, uuid.UUID, podexec.Options) (*podexec.Session, error) { return session, nil },
		func(context.Context, *podexec.Session, podexec.Streams) error {
			return errors.New("container not running")
		},
	)

	conn, _, err := a.dialExec(token, "/v1/environments/"+envID.String()+"/exec?service=web")
	require.NoError(t, err)
	defer conn.Close()

	transcript := readTranscript(t, conn)
	require.Equal(t, execproto.ControlError, transcript.terminal.Type)
	require.Equal(t, execproto.ErrCodeExecFailed, transcript.terminal.ErrCode)
	require.Contains(t, transcript.terminal.Message, "container not running")
}

func TestExecClientDisconnectCancelsStream(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	envID := a.execEnvironment(t, token)

	cancelled := make(chan struct{})
	session := &podexec.Session{Namespace: "ns", Pod: "web-1", Container: "web", Command: []string{"sh"}}
	a.execFake.script(
		func(context.Context, uuid.UUID, podexec.Options) (*podexec.Session, error) { return session, nil },
		func(ctx context.Context, _ *podexec.Session, _ podexec.Streams) error {
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		},
	)

	conn, _, err := a.dialExec(token, "/v1/environments/"+envID.String()+"/exec?service=web")
	require.NoError(t, err)
	conn.Close()

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("client disconnect did not cancel the exec stream")
	}
}

func TestExecResizeReachesQueue(t *testing.T) {
	a := newTestAPI(t)
	a.createUser("nick@example.com", "hunter2hunter2")
	token := a.login("nick@example.com", "hunter2hunter2")
	envID := a.execEnvironment(t, token)

	sizes := make(chan [2]uint16, 1)
	a.execFake.script(
		func(_ context.Context, _ uuid.UUID, opts podexec.Options) (*podexec.Session, error) {
			return &podexec.Session{Namespace: "ns", Pod: "web-1", Container: "web",
				Command: []string{"sh"}, TTY: opts.TTY}, nil
		},
		func(_ context.Context, _ *podexec.Session, streams podexec.Streams) error {
			require.NotNil(t, streams.Resize, "tty sessions must carry a resize queue")
			size := streams.Resize.Next()
			if size != nil {
				sizes <- [2]uint16{size.Width, size.Height}
			}
			return nil
		},
	)

	conn, _, err := a.dialExec(token, "/v1/environments/"+envID.String()+"/exec?service=web&tty=true")
	require.NoError(t, err)
	defer conn.Close()

	resize, err := execproto.EncodeControl(execproto.Control{Type: execproto.ControlResize, Cols: 120, Rows: 40})
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, resize))

	select {
	case size := <-sizes:
		require.Equal(t, [2]uint16{120, 40}, size)
	case <-time.After(5 * time.Second):
		t.Fatal("resize never reached the terminal size queue")
	}
	transcript := readTranscript(t, conn)
	require.Equal(t, execproto.ControlExit, transcript.terminal.Type)
}
