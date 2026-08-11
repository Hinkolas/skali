// Package execproto defines the wire protocol of one interactive exec
// session between a client and skalid, carried over a WebSocket. Every
// message is a binary frame whose first byte selects a channel: raw stdin,
// stdout, and stderr bytes flow on their own channels, and a single JSON
// control channel carries everything else (resize, stdin half-close, and
// the terminal exit or error frame). The package is shared by the server
// handler and the CLI client and must import neither.
package execproto

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Channels. Stdin flows client to server; stdout and stderr flow server to
// client (stderr is unused when the session runs a TTY, which merges the
// streams remotely). Control flows both ways.
const (
	ChannelStdin   byte = 0
	ChannelStdout  byte = 1
	ChannelStderr  byte = 2
	ChannelControl byte = 3
)

// Control message types.
const (
	// ControlResize (client to server) reports the local terminal size.
	// The client sends the initial size right after connecting and again
	// on every change. Ignored for sessions without a TTY.
	ControlResize = "resize"
	// ControlStdinEOF (client to server) half-closes the input: the remote
	// process sees EOF on stdin while output keeps flowing. This is what
	// terminates piped invocations such as `exec app -- psql < dump.sql`.
	ControlStdinEOF = "stdin_eof"
	// ControlExit (server to client) is the terminal frame of a session
	// whose process ran to completion; Code carries its exit status. The
	// server closes the connection after sending it.
	ControlExit = "exit"
	// ControlError (server to client) is the terminal frame of a session
	// that failed for infrastructure reasons (container gone, command not
	// found in the image, kubelet error). The server closes the connection
	// after sending it.
	ControlError = "error"
)

// ErrCodeExecFailed is the machine code carried by ControlError frames.
const ErrCodeExecFailed = "exec_failed"

// Control is the single JSON message shape of the control channel; Type
// discriminates which of the remaining fields are meaningful.
type Control struct {
	Type    string `json:"type"`
	Cols    uint16 `json:"cols,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
	Code    int    `json:"code,omitempty"`
	ErrCode string `json:"err_code,omitempty"`
	Message string `json:"message,omitempty"`
}

// EncodeData frames a payload for one data channel. The payload is copied:
// callers routinely reuse their read buffer for the next chunk while the
// frame is still queued for writing.
func EncodeData(channel byte, p []byte) []byte {
	frame := make([]byte, 1+len(p))
	frame[0] = channel
	copy(frame[1:], p)
	return frame
}

// EncodeControl frames one control message.
func EncodeControl(c Control) ([]byte, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("execproto: encode control: %w", err)
	}
	return EncodeData(ChannelControl, payload), nil
}

// Decode splits a received frame into its channel and payload. The payload
// aliases the input. Empty frames are invalid: every message carries at
// least the channel byte.
func Decode(message []byte) (channel byte, payload []byte, err error) {
	if len(message) == 0 {
		return 0, nil, errors.New("execproto: empty frame")
	}
	return message[0], message[1:], nil
}

// ParseControl decodes a control-channel payload. Unknown Type values pass
// through untouched so either side can ignore messages from a newer peer.
func ParseControl(payload []byte) (Control, error) {
	var c Control
	if err := json.Unmarshal(payload, &c); err != nil {
		return Control{}, fmt.Errorf("execproto: parse control: %w", err)
	}
	return c, nil
}
