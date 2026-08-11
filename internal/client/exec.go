package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	// writeMu serializes every write to the connection (stdin, control,
	// pongs, close): gorilla forbids concurrent writers.
	writeMu sync.Mutex
}

// Exec opens an interactive exec session against one environment. Handshake
// failures decode the normal error envelope, so callers branch on *APIError
// codes (reauth_required, no_ready_pod, not_found) exactly like any other
// endpoint.
func (c *Client) Exec(ctx context.Context, environmentID string, opts ExecOptions) (*ExecSession, error) {
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

	header := http.Header{}
	if c.token != "" {
		header.Set("Authorization", "Bearer "+c.token)
	}
	if c.userAgent != "" {
		header.Set("User-Agent", c.userAgent)
	}

	// Never c.http: its 15s client timeout would sever the session. The
	// dialer bounds only the handshake.
	dialer := &websocket.Dialer{
		NetDialContext:   localhostDialContext(),
		HandshakeTimeout: execHandshakeTimeout,
		Proxy:            http.ProxyFromEnvironment,
		ReadBufferSize:   32 * 1024,
		WriteBufferSize:  32 * 1024,
	}
	wsURL := target.String() + "/v1/environments/" + url.PathEscape(environmentID) + "/exec?" + query.Encode()
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
			message := websocket.FormatCloseMessage(websocket.CloseGoingAway, "")
			_ = s.write(websocket.CloseMessage, message)
			s.conn.Close()
		case <-done:
		}
	}()

	if stdin != nil {
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
					if s.write(websocket.BinaryMessage, frame) != nil {
						return
					}
				}
				if err != nil {
					if errors.Is(err, io.EOF) {
						// Half-close: the remote process sees EOF while
						// output keeps flowing.
						if frame, encodeErr := execproto.EncodeControl(execproto.Control{Type: execproto.ControlStdinEOF}); encodeErr == nil {
							_ = s.write(websocket.BinaryMessage, frame)
						}
					}
					return
				}
			}
		}()
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
	// The default ping handler writes the pong directly, racing the pumps;
	// route it through the write mutex and refresh the liveness deadline.
	s.conn.SetPingHandler(func(appData string) error {
		refresh()
		return s.write(websocket.PongMessage, []byte(appData))
	})

	for {
		_, message, err := s.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
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
			case execproto.ControlExit:
				if control.Code != 0 {
					return &ExecExitError{Code: control.Code}
				}
				return nil
			case execproto.ControlError:
				return fmt.Errorf("exec failed: %s", strings.TrimSpace(control.Message))
			}
		}
	}
}

func (s *ExecSession) write(messageType int, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(execWriteTimeout))
	return s.conn.WriteMessage(messageType, data)
}
