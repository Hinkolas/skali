package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"
	kexec "k8s.io/client-go/util/exec"

	"github.com/Hinkolas/skali/internal/auth"
	"github.com/Hinkolas/skali/internal/execproto"
	"github.com/Hinkolas/skali/internal/podexec"
	"github.com/Hinkolas/skali/internal/sqlsession"
)

// ExecService is the resolve-and-stream surface the exec handler needs:
// *podexec.Service in production, a fake in tests.
type ExecService interface {
	Resolve(ctx context.Context, environmentID uuid.UUID, opts podexec.Options) (*podexec.Session, error)
	Stream(ctx context.Context, session *podexec.Session, streams podexec.Streams) error
}

// Exec session tuning. The ping cadence matches the SSE heartbeat; the read
// deadline reaps sessions whose peer vanished without a close handshake so
// the kubelet exec is not held forever.
const (
	execPingInterval = 15 * time.Second
	execReadTimeout  = 60 * time.Second
	execWriteTimeout = 10 * time.Second
)

// execHandlers serves interactive exec sessions: a sanctioned
// request-time cluster pass-through like credential reveal and runtime
// logs, carried over a WebSocket speaking internal/execproto.
type execHandlers struct {
	exec     ExecService
	auth     *auth.Service
	upgrader websocket.Upgrader
}

func newExecHandlers(exec ExecService, auth *auth.Service) *execHandlers {
	return &execHandlers{exec: exec, auth: auth, upgrader: sessionUpgrader()}
}

// sessionUpgrader is the WebSocket upgrader of every execproto session
// (exec and SQL).
func sessionUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
		// Cookie requests are checked before resolution in the handlers.
		// CLI bearer clients do not need an Origin header.
		CheckOrigin: func(r *http.Request) bool { return !cookieAuthFrom(r) || sameOrigin(r) },
	}
}

// GET /v1/environments/{id}/exec?service=web&cmd=...&cmd=...&tty=true
// Mounted outside the request timeout. Everything validates and resolves
// before the upgrade, so every failure is the normal JSON envelope on the
// handshake response; after the 101 the connection speaks execproto.
func (h *execHandlers) open(w http.ResponseWriter, r *http.Request) {
	if cookieAuthFrom(r) && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, codeForbidden, "websocket requires a same-origin Origin")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	tty := false
	if raw := query.Get("tty"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "invalid tty value")
			return
		}
		tty = parsed
	}
	opts := podexec.Options{
		Service:   query.Get("service"),
		Pod:       query.Get("pod"),
		Container: query.Get("container"),
		Command:   query["cmd"],
		TTY:       tty,
	}

	if !requireFreshWhenProtected(w, r, h.auth) {
		return
	}

	session, err := h.exec.Resolve(r.Context(), id, opts)
	if err != nil {
		h.writeResolveError(r.Context(), w, err)
		return
	}

	conn, ok := upgradeSession(w, r, &h.upgrader)
	if !ok {
		return
	}
	defer conn.Close()

	start := time.Now()
	streamErr := bridgeSession(r.Context(), conn, session.TTY, func(ctx context.Context, streams podexec.Streams, ready func()) error {
		ready()
		return h.exec.Stream(ctx, session, streams)
	})

	code := 0
	if exit, ok := errors.AsType[kexec.CodeExitError](streamErr); ok {
		code = exit.Code
		streamErr = nil
	}
	user := UserFrom(r.Context())
	slog.InfoContext(r.Context(), "pod exec",
		"user", user.Email,
		"user_id", user.ID,
		"environment", id,
		"service", opts.Service,
		"pod", session.Pod,
		"container", session.Container,
		"command", session.Command,
		"tty", session.TTY,
		"duration_ms", time.Since(start).Milliseconds(),
		"exit_code", code,
		"err", streamErr,
	)
}

// requireFreshWhenProtected is the sudo gate of a session that can change
// what the deploy policy protects (a shell in a container, SQL against a
// database): a promote-only environment asks for sudo mode, a direct one is
// covered by the maintain role alone. It writes the refusal and reports
// whether the request may continue.
func requireFreshWhenProtected(w http.ResponseWriter, r *http.Request, authSvc *auth.Service) bool {
	if envGrant := environmentGrantFrom(r.Context()); envGrant != nil && envGrant.Protected() {
		if sess := SessionFrom(r.Context()); sess == nil || !authSvc.IsSessionFresh(sess) {
			writeError(w, http.StatusForbidden, codeReauthRequired,
				"recent authentication required: environment "+envGrant.Name+" is promote-only")
			return false
		}
	}
	return true
}

// upgradeSession answers the WebSocket handshake. gorilla composes the 101
// itself and drops whatever middleware put on w.Header(); the platform
// headers are carried over explicitly so instance pinning covers the
// handshake response too. On failure Upgrade has already written the HTTP
// error.
func upgradeSession(w http.ResponseWriter, r *http.Request, upgrader *websocket.Upgrader) (*websocket.Conn, bool) {
	responseHeader := http.Header{}
	for _, name := range []string{InstanceHeader, VersionHeader, "Set-Cookie"} {
		if value := w.Header().Get(name); value != "" {
			responseHeader.Set(name, value)
		}
	}
	conn, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		return nil, false
	}
	return conn, true
}

func (h *execHandlers) writeResolveError(ctx context.Context, w http.ResponseWriter, err error) {
	var noReady *podexec.NoReadyPodError
	switch {
	case errors.Is(err, podexec.ErrNoCluster):
		writeError(w, http.StatusServiceUnavailable, codeNodeUnreachable,
			"no cluster is connected; exec is unavailable")
	case errors.Is(err, podexec.ErrEnvironmentNotFound), errors.Is(err, podexec.ErrPodNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, podexec.ErrInvalidOptions):
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
	case errors.As(err, &noReady):
		writeError(w, http.StatusConflict, codeNoReadyPod, noReady.Error())
	default:
		writeInternalError(ctx, w, "exec resolve", err)
	}
}

// bridgeSession pumps one established WebSocket against a session's
// process, which run drives over the given streams, and returns the
// outcome (nil, *kexec.CodeExitError, or an infrastructure error). It owns
// the session teardown: the terminal control frame, the close handshake,
// and cancelling the upstream process when the client vanishes. The
// connection is live (pinged, read) before run starts, so a run that first
// starts its target (an SQL client pod) keeps the client attached; run
// calls ready once its process reads stdin, which sends the ready frame
// clients of such sessions hold their stdin for.
func bridgeSession(ctx context.Context, conn *websocket.Conn, tty bool,
	run func(ctx context.Context, streams podexec.Streams, ready func()) error) error {
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A single writer goroutine owns the connection's write side: gorilla
	// forbids concurrent writes, and stdout, stderr, control frames, and
	// pings all race otherwise. A nil frame tells the writer to perform the
	// close handshake and exit.
	outbound := make(chan []byte, 32)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		ping := time.NewTicker(execPingInterval)
		defer ping.Stop()
		for {
			select {
			case frame := <-outbound:
				if frame == nil {
					message := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
					_ = conn.SetWriteDeadline(time.Now().Add(execWriteTimeout))
					_ = conn.WriteMessage(websocket.CloseMessage, message)
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(execWriteTimeout))
				if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					cancel()
					return
				}
			case <-ping.C:
				_ = conn.SetWriteDeadline(time.Now().Add(execWriteTimeout))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	stdinReader, stdinWriter := io.Pipe()
	defer stdinWriter.Close()

	streams := podexec.Streams{
		Stdin:  stdinReader,
		Stdout: &execChannelWriter{channel: execproto.ChannelStdout, outbound: outbound, done: execCtx.Done()},
		Stderr: &execChannelWriter{channel: execproto.ChannelStderr, outbound: outbound, done: execCtx.Done()},
	}
	var resize chan remotecommand.TerminalSize
	if tty {
		resize = make(chan remotecommand.TerminalSize, 4)
		streams.Resize = &execSizeQueue{sizes: resize, done: execCtx.Done()}
	}

	var readyOnce sync.Once
	ready := func() {
		readyOnce.Do(func() {
			frame, err := execproto.EncodeControl(execproto.Control{Type: execproto.ControlReady})
			if err != nil {
				return
			}
			select {
			case outbound <- frame:
			case <-writerDone:
			case <-execCtx.Done():
			}
		})
	}
	execDone := make(chan error, 1)
	go func() {
		execDone <- run(execCtx, streams, ready)
	}()

	// Read loop: stdin and control frames in, until the peer closes or the
	// deadline reaps a vanished one. Its exit cancels the upstream exec.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancel()
		conn.SetReadLimit(1 << 20)
		refresh := func() { _ = conn.SetReadDeadline(time.Now().Add(execReadTimeout)) }
		refresh()
		conn.SetPongHandler(func(string) error { refresh(); return nil })
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			refresh()
			channel, payload, err := execproto.Decode(message)
			if err != nil {
				continue
			}
			switch channel {
			case execproto.ChannelStdin:
				// A write error means the remote closed stdin; keep
				// draining so control frames still flow. The write blocks
				// until the process reads (or, for an SQL session, until
				// its client has started); the peer was alive when the
				// frame arrived, so the wait does not count against it.
				_, _ = stdinWriter.Write(payload)
				refresh()
			case execproto.ChannelControl:
				control, err := execproto.ParseControl(payload)
				if err != nil {
					continue
				}
				switch control.Type {
				case execproto.ControlStdinEOF:
					_ = stdinWriter.Close()
				case execproto.ControlResize:
					if resize == nil {
						continue
					}
					select {
					case resize <- remotecommand.TerminalSize{Width: control.Cols, Height: control.Rows}:
					default: // never block the read loop on a slow resize consumer
					}
				}
			}
		}
	}()

	streamErr := <-execDone
	// Nothing reads stdin any more; a read loop parked in a pipe write (a
	// process that ended, or an SQL client that never started, with input
	// already sent) gets ErrClosedPipe and goes back to reading frames.
	_ = stdinReader.CloseWithError(io.ErrClosedPipe)

	// Terminal frame: the process's own exit status, or the infrastructure
	// failure. A cancelled session (client gone) sends nothing; the write
	// side is dead anyway.
	terminal := execproto.Control{Type: execproto.ControlExit}
	if exit, ok := errors.AsType[kexec.CodeExitError](streamErr); ok {
		terminal.Code = exit.Code
	} else if startErr, ok := errors.AsType[*sqlsession.StartError](streamErr); ok {
		terminal = execproto.Control{
			Type:    execproto.ControlError,
			ErrCode: execproto.ErrCodeStartFailed,
			Message: startErr.Error(),
		}
	} else if streamErr != nil {
		terminal = execproto.Control{
			Type:    execproto.ControlError,
			ErrCode: execproto.ErrCodeExecFailed,
			Message: streamErr.Error(),
		}
	}
	if frame, err := execproto.EncodeControl(terminal); err == nil {
		select {
		case outbound <- frame:
		case <-writerDone:
		}
	}
	select {
	case outbound <- nil: // close handshake
	case <-writerDone:
	}
	<-writerDone
	conn.Close() // unblocks the read loop if the peer never answers the close
	<-readerDone
	return streamErr
}

// execChannelWriter frames one output stream onto the writer goroutine.
type execChannelWriter struct {
	channel  byte
	outbound chan<- []byte
	done     <-chan struct{}
}

func (w *execChannelWriter) Write(p []byte) (int, error) {
	// EncodeData copies: remotecommand reuses its read buffer.
	select {
	case w.outbound <- execproto.EncodeData(w.channel, p):
		return len(p), nil
	case <-w.done:
		return 0, errors.New("exec session closed")
	}
}

// execSizeQueue adapts the resize channel to remotecommand's pull API; a
// nil return ends its resize loop.
type execSizeQueue struct {
	sizes <-chan remotecommand.TerminalSize
	done  <-chan struct{}
}

func (q *execSizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case size := <-q.sizes:
		return &size
	case <-q.done:
		return nil
	}
}
