package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/execproto"
)

// execTestServer runs script against every established exec WebSocket.
func execTestServer(t *testing.T, script func(t *testing.T, r *http.Request, conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		script(t, r, conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sendControl(t *testing.T, conn *websocket.Conn, control execproto.Control) {
	t.Helper()
	frame, err := execproto.EncodeControl(control)
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, frame))
}

func TestExecRunDemuxAndExit(t *testing.T) {
	srv := execTestServer(t, func(t *testing.T, r *http.Request, conn *websocket.Conn) {
		require.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		require.Equal(t, "web", r.URL.Query().Get("service"))
		require.Equal(t, []string{"echo", "hi"}, r.URL.Query()["cmd"])
		require.NoError(t, conn.WriteMessage(websocket.BinaryMessage,
			execproto.EncodeData(execproto.ChannelStdout, []byte("out!"))))
		require.NoError(t, conn.WriteMessage(websocket.BinaryMessage,
			execproto.EncodeData(execproto.ChannelStderr, []byte("err!"))))
		sendControl(t, conn, execproto.Control{Type: execproto.ControlExit})
	})

	c := New(srv.URL, "tok", "test-agent")
	session, err := c.Exec(context.Background(), "env-1", ExecOptions{Service: "web", Command: []string{"echo", "hi"}})
	require.NoError(t, err)
	defer session.Close()

	var stdout, stderr strings.Builder
	require.NoError(t, session.Run(context.Background(), nil, &stdout, &stderr, nil))
	require.Equal(t, "out!", stdout.String())
	require.Equal(t, "err!", stderr.String())
}

func TestExecRunExitCode(t *testing.T) {
	srv := execTestServer(t, func(t *testing.T, _ *http.Request, conn *websocket.Conn) {
		sendControl(t, conn, execproto.Control{Type: execproto.ControlExit, Code: 5})
	})

	c := New(srv.URL, "tok", "")
	session, err := c.Exec(context.Background(), "env-1", ExecOptions{Service: "web"})
	require.NoError(t, err)
	defer session.Close()

	err = session.Run(context.Background(), nil, nil, nil, nil)
	var exit *ExecExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 5, exit.Code)
}

func TestExecRunInfrastructureError(t *testing.T) {
	srv := execTestServer(t, func(t *testing.T, _ *http.Request, conn *websocket.Conn) {
		sendControl(t, conn, execproto.Control{
			Type: execproto.ControlError, ErrCode: execproto.ErrCodeExecFailed, Message: "container gone",
		})
	})

	c := New(srv.URL, "tok", "")
	session, err := c.Exec(context.Background(), "env-1", ExecOptions{Service: "web"})
	require.NoError(t, err)
	defer session.Close()

	err = session.Run(context.Background(), nil, nil, nil, nil)
	require.ErrorContains(t, err, "container gone")
}

func TestExecRunStdinEOF(t *testing.T) {
	srv := execTestServer(t, func(t *testing.T, _ *http.Request, conn *websocket.Conn) {
		// Expect the piped bytes, then the half-close, then answer with an
		// exit frame like a completed remote process.
		var received strings.Builder
		for {
			_, message, err := conn.ReadMessage()
			require.NoError(t, err)
			channel, payload, err := execproto.Decode(message)
			require.NoError(t, err)
			if channel == execproto.ChannelStdin {
				received.Write(payload)
				continue
			}
			require.Equal(t, execproto.ChannelControl, channel)
			control, err := execproto.ParseControl(payload)
			require.NoError(t, err)
			require.Equal(t, execproto.ControlStdinEOF, control.Type)
			break
		}
		require.Equal(t, "piped input", received.String())
		require.NoError(t, conn.WriteMessage(websocket.BinaryMessage,
			execproto.EncodeData(execproto.ChannelStdout, []byte("done"))))
		sendControl(t, conn, execproto.Control{Type: execproto.ControlExit})
	})

	c := New(srv.URL, "tok", "")
	session, err := c.Exec(context.Background(), "env-1", ExecOptions{Service: "web", Command: []string{"cat"}})
	require.NoError(t, err)
	defer session.Close()

	var stdout strings.Builder
	require.NoError(t, session.Run(context.Background(), strings.NewReader("piped input"), &stdout, nil, nil))
	require.Equal(t, "done", stdout.String())
}

func TestExecRunResize(t *testing.T) {
	got := make(chan execproto.Control, 1)
	srv := execTestServer(t, func(t *testing.T, _ *http.Request, conn *websocket.Conn) {
		_, message, err := conn.ReadMessage()
		require.NoError(t, err)
		channel, payload, err := execproto.Decode(message)
		require.NoError(t, err)
		require.Equal(t, execproto.ChannelControl, channel)
		control, err := execproto.ParseControl(payload)
		require.NoError(t, err)
		got <- control
		sendControl(t, conn, execproto.Control{Type: execproto.ControlExit})
	})

	c := New(srv.URL, "tok", "")
	session, err := c.Exec(context.Background(), "env-1", ExecOptions{Service: "web", TTY: true})
	require.NoError(t, err)
	defer session.Close()

	resize := make(chan TerminalSize, 1)
	resize <- TerminalSize{Cols: 100, Rows: 30}
	require.NoError(t, session.Run(context.Background(), nil, nil, nil, resize))

	control := <-got
	require.Equal(t, execproto.ControlResize, control.Type)
	require.Equal(t, uint16(100), control.Cols)
	require.Equal(t, uint16(30), control.Rows)
}

func TestExecBadHandshakeDecodesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"reauth_required","message":"recent authentication required"}}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "tok", "")
	_, err := c.Exec(context.Background(), "env-1", ExecOptions{Service: "web"})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "reauth_required", apiErr.Code)
	require.Equal(t, http.StatusForbidden, apiErr.Status)
}

func TestExecInstancePinOnHandshake(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The header must ride the 101 itself: gorilla drops w.Header().
		conn, err := upgrader.Upgrade(w, r, http.Header{InstanceHeader: []string{"instance-b"}})
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(50 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "tok", "")
	c.PinInstance("instance-a", nil)
	_, err := c.Exec(context.Background(), "env-1", ExecOptions{Service: "web"})
	var mismatch *InstanceMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, "instance-b", mismatch.Observed)
}

func TestExecRunContextCancel(t *testing.T) {
	srv := execTestServer(t, func(t *testing.T, _ *http.Request, conn *websocket.Conn) {
		// Never send a terminal frame; wait for the client to vanish.
		_, _, _ = conn.ReadMessage()
	})

	c := New(srv.URL, "tok", "")
	session, err := c.Exec(context.Background(), "env-1", ExecOptions{Service: "web"})
	require.NoError(t, err)
	defer session.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err = session.Run(ctx, nil, nil, nil, nil)
	require.ErrorIs(t, err, context.Canceled)
}
