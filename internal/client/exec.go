package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Hinkolas/skali/internal/execproto"
)

// Exec session tuning, mirroring the server side: the peer pings every 15s,
// so a 60s read deadline only reaps genuinely dead connections.
const (
	execHandshakeTimeout = 15 * time.Second
	execReadTimeout      = 60 * time.Second
	execWriteTimeout     = 10 * time.Second
	// controlWriteTimeout bounds pongs and the close frame, which must
	// never hold up the read loop or cancellation for long.
	controlWriteTimeout = time.Second
)

// ExecOptions is one exec invocation's target and command.
type ExecOptions struct {
	Service   string
	Pod       string
	Container string
	Command   []string // empty runs the server's default shell
	TTY       bool
}

// TerminalSize is one local terminal dimension update.
type TerminalSize struct {
	Cols uint16
	Rows uint16
}

// ExecExitError reports that the remote command ran and exited nonzero.
// cmd/skali maps it onto the local exit status without printing an error
// line: the remote process already wrote its own stderr.
type ExecExitError struct {
	Code int
}

func (e *ExecExitError) Error() string {
	return fmt.Sprintf("command exited with code %d", e.Code)
}

// ExecSession is one established exec WebSocket. Run drives it; Close
// releases it early.
type ExecSession struct {
	conn *websocket.Conn
	// writeMu serializes the data writes (stdin, stdin EOF, resize):
	// gorilla forbids concurrent writers. Pongs and the close frame use
	// WriteControl, which it allows alongside them.
	writeMu sync.Mutex
	// holdStdin defers stdin forwarding until the server's ready frame:
	// an SQL session's process starts only after the handshake (its
	// client pod may take minutes), and a script pushed before then only
	// fills buffers until the writes time out.
	holdStdin bool
}

// SQLOptions is one SQL session's shape. The script itself is not part of
// it: a batch session sends it as the session's stdin.
type SQLOptions struct {
	Format   string // psql (default), csv, or json
	TTY      bool   // interactive psql
	ReadOnly bool
	// StatementTimeout sets statement_timeout; zero keeps the server's.
	StatementTimeout time.Duration
}

// Exec opens an interactive exec session against one environment. Handshake
// failures decode the normal error envelope, so callers branch on *APIError
// codes (reauth_required, no_ready_pod, not_found) exactly like any other
// endpoint.
func (c *Client) Exec(ctx context.Context, environmentID string, opts ExecOptions) (*ExecSession, error) {
	query := url.Values{}
	if opts.Service != "" {
		query.Set("service", opts.Service)
	}
	if opts.Pod != "" {
		query.Set("pod", opts.Pod)
	}
	if opts.Container != "" {
		query.Set("container", opts.Container)
	}
	if opts.TTY {
		query.Set("tty", "true")
	}
	for _, argument := range opts.Command {
		query.Add("cmd", argument)
	}
	return c.dialSession(ctx, "/v1/environments/"+url.PathEscape(environmentID)+"/exec", query)
}

// SQL opens an SQL session against one database service of an environment.
// It speaks the exec protocol, so the returned session runs exactly like an
// exec session; the server starts the database client after the handshake.
// Handshake failures decode the normal error envelope (not_found for an
// unknown database, conflict while it provisions, reauth_required).
func (c *Client) SQL(ctx context.Context, environmentID, database string, opts SQLOptions) (*ExecSession, error) {
	query := url.Values{}
	if opts.Format != "" {
		query.Set("format", opts.Format)
	}
	if opts.TTY {
		query.Set("tty", "true")
	}
	if opts.ReadOnly {
		query.Set("read_only", "true")
	}
	if opts.StatementTimeout > 0 {
		query.Set("statement_timeout", strconv.FormatInt(opts.StatementTimeout.Milliseconds(), 10))
	}
	session, err := c.dialSession(ctx, "/v1/environments/"+url.PathEscape(environmentID)+
		"/databases/"+url.PathEscape(database)+"/sql", query)
	if err != nil {
		return nil, err
	}
	session.holdStdin = true
	return session, nil
}

// dialSession opens one execproto WebSocket at path.
func (c *Client) dialSession(ctx context.Context, path string, query url.Values) (*ExecSession, error) {
	target, err := url.Parse(c.base)
	if err != nil {
		return nil, fmt.Errorf("client: parse master url: %w", err)
	}
	switch target.Scheme {
	case "http":
		target.Scheme = "ws"
	case "https":
		target.Scheme = "wss"
	default:
		return nil, fmt.Errorf("client: master %s has no websocket scheme", c.base)
	}

	header := http.Header{}
	c.stamp(header)

	// The upgrade only exists in HTTP/1.1: an edge that negotiates h2
	// through ALPN answers it in frames gorilla cannot read.
	var tlsConfig *tls.Config
	if c.tls != nil {
		tlsConfig = c.tls.Clone()
		tlsConfig.NextProtos = []string{"http/1.1"}
	}
	// Never c.http: its 15s client timeout would sever the session. The
	// dialer bounds only the handshake.
	dialer := &websocket.Dialer{
		NetDialContext:   localhostDialContext(),
		TLSClientConfig:  tlsConfig,
		HandshakeTimeout: execHandshakeTimeout,
		Proxy:            http.ProxyFromEnvironment,
		ReadBufferSize:   32 * 1024,
		WriteBufferSize:  32 * 1024,
	}
	wsURL := target.String() + path + "?" + query.Encode()
	conn, res, err := dialer.DialContext(ctx, wsURL, header)
	if res != nil {
		// Instance pinning covers the handshake response, 101 or error: an
		// identity change explains whatever error rode along.
		if pinErr := c.checkInstance(res); pinErr != nil {
			if conn != nil {
				conn.Close()
			}
			return nil, pinErr
		}
	}
	if err != nil {
		if errors.Is(err, websocket.ErrBadHandshake) && res != nil {
			raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
			res.Body.Close()
			return nil, decodeErrorEnvelope(res.StatusCode, raw)
		}
		return nil, fmt.Errorf("client: %s unreachable: %w", c.base, err)
	}
	return &ExecSession{conn: conn}, nil
}

// Close releases the session's connection.
func (s *ExecSession) Close() error {
	return s.conn.Close()
}

// Run pumps the local streams until the remote process ends. It returns nil
// on exit 0, *ExecExitError on a nonzero exit, and other errors for
// infrastructure failures or local cancellation. stdin may be nil; resize
// may be nil for sessions without a TTY.
func (s *ExecSession) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, resize <-chan TerminalSize) error {
	done := make(chan struct{})
	defer close(done)

	// Local cancellation closes the connection: the server cancels the
	// upstream exec when its read loop dies.
	go func() {
		select {
		case <-ctx.Done():
			// WriteControl does not queue behind a stdin write the server
			// is not taking (writeMu); the close frame gets a short try,
			// then the connection goes regardless.
			message := websocket.FormatCloseMessage(websocket.CloseGoingAway, "")
			_ = s.conn.WriteControl(websocket.CloseMessage, message, time.Now().Add(controlWriteTimeout))
			s.conn.Close()
		case <-done:
		}
	}()

	// A stdin that fails (a read error, a write the server never takes)
	// ends the session with that error: dropping the rest silently would
	// leave the remote process waiting for an EOF that never comes.
	var stdinFailure atomic.Pointer[error]
	failStdin := func(err error) {
		stdinFailure.CompareAndSwap(nil, &err)
		s.conn.Close()
	}
	pumpStdin := func() {
		if stdin == nil {
			return
		}
		// The pump stays blocked in stdin.Read when the session ends first
		// (an interactive os.Stdin has no unblockable read); the goroutine
		// is accepted as leaked since the process is about to exit or the
		// next read ends it.
		go func() {
			buffer := make([]byte, 32*1024)
			for {
				n, err := stdin.Read(buffer)
				if n > 0 {
					frame := execproto.EncodeData(execproto.ChannelStdin, buffer[:n])
					// Stdin waits for the remote process to read it, which
					// takes as long as it takes (psql runs a long statement
					// before it reads the next one), so its writes carry no
					// deadline. Peer liveness is the read loop's deadline;
					// cancellation closes the connection under the write.
					if writeErr := s.writeWithin(websocket.BinaryMessage, frame, 0); writeErr != nil {
						select {
						case <-done:
						default:
							failStdin(fmt.Errorf("forward stdin: %w", writeErr))
						}
						return
					}
				}
				if err != nil {
					if !errors.Is(err, io.EOF) {
						failStdin(fmt.Errorf("read stdin: %w", err))
						return
					}
					// Half-close: the remote process sees EOF while output
					// keeps flowing. It queues behind the input like any
					// stdin frame, so it waits as long as they do.
					frame, encodeErr := execproto.EncodeControl(execproto.Control{Type: execproto.ControlStdinEOF})
					if encodeErr != nil {
						failStdin(encodeErr)
						return
					}
					if writeErr := s.writeWithin(websocket.BinaryMessage, frame, 0); writeErr != nil {
						select {
						case <-done:
						default:
							failStdin(fmt.Errorf("forward stdin: %w", writeErr))
						}
					}
					return
				}
			}
		}()
	}
	var pumpOnce sync.Once
	if !s.holdStdin {
		pumpOnce.Do(pumpStdin)
	}

	if resize != nil {
		go func() {
			for {
				select {
				case size, open := <-resize:
					if !open {
						return
					}
					frame, err := execproto.EncodeControl(execproto.Control{
						Type: execproto.ControlResize, Cols: size.Cols, Rows: size.Rows,
					})
					if err != nil || s.write(websocket.BinaryMessage, frame) != nil {
						return
					}
				case <-done:
					return
				}
			}
		}()
	}

	s.conn.SetReadLimit(1 << 20)
	refresh := func() { _ = s.conn.SetReadDeadline(time.Now().Add(execReadTimeout)) }
	refresh()
	// Pongs go out through WriteControl, which gorilla allows next to the
	// pumps' writes, with a short bound: while a stdin write waits for the
	// remote process to read, the read loop must keep draining output, or
	// the server's output writes stall in turn. A pong that cannot go out
	// is skipped; the server counts every frame it reads as liveness.
	s.conn.SetPingHandler(func(appData string) error {
		refresh()
		_ = s.conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(controlWriteTimeout))
		return nil
	})

	for {
		_, message, err := s.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if failure := stdinFailure.Load(); failure != nil {
				return *failure
			}
			return fmt.Errorf("client: exec connection closed unexpectedly: %w", err)
		}
		refresh()
		channel, payload, err := execproto.Decode(message)
		if err != nil {
			continue
		}
		switch channel {
		case execproto.ChannelStdout:
			if stdout != nil {
				if _, err := stdout.Write(payload); err != nil {
					return fmt.Errorf("client: write stdout: %w", err)
				}
			}
		case execproto.ChannelStderr:
			if stderr != nil {
				if _, err := stderr.Write(payload); err != nil {
					return fmt.Errorf("client: write stderr: %w", err)
				}
			}
		case execproto.ChannelControl:
			control, err := execproto.ParseControl(payload)
			if err != nil {
				continue
			}
			switch control.Type {
			case execproto.ControlReady:
				pumpOnce.Do(pumpStdin)
			case execproto.ControlExit:
				if control.Code != 0 {
					return &ExecExitError{Code: control.Code}
				}
				return nil
			case execproto.ControlError:
				message := strings.TrimSpace(control.Message)
				if control.ErrCode == execproto.ErrCodeExecFailed || control.ErrCode == "" {
					return fmt.Errorf("exec failed: %s", message)
				}
				// Other codes carry a self-describing message.
				return errors.New(message)
			}
		}
	}
}

func (s *ExecSession) write(messageType int, data []byte) error {
	return s.writeWithin(messageType, data, execWriteTimeout)
}

// writeWithin writes one data message; a zero timeout waits indefinitely.
func (s *ExecSession) writeWithin(messageType int, data []byte, timeout time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	_ = s.conn.SetWriteDeadline(deadline)
	return s.conn.WriteMessage(messageType, data)
}
