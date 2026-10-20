//go:build darwin || linux

package devremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// macOS lets only root listen below port 1024 on a specific address, which
// the proxy's loopback edge ports and expose's chosen address both are.
// The listen helper is the part of skali that runs with those rights: the
// CLI starts it as root (through sudo), it binds the addresses it was
// given, passes the listening sockets back over a Unix socket, and exits.
// The session itself keeps running as the user, with the user's state and
// trust stores.

// handoffReply is the helper's message next to the sockets it passes.
type handoffReply struct {
	// Error, Errno, and Listen describe the address that could not be
	// bound; no sockets come with a reply that has them.
	Error  string `json:"error,omitempty"`
	Errno  int    `json:"errno,omitempty"`
	Listen string `json:"listen,omitempty"`
}

// acceptWait bounds the wait for the helper's connection once it exited:
// it connected before it exited, so the connection is queued by then or
// never comes. readWait bounds reading its message.
const (
	acceptWait = time.Second
	readWait   = 5 * time.Second
)

// maxHelperAddresses caps what the helper binds: a session has two routes.
const maxHelperAddresses = 4

// ListenThrough binds every route in a listen helper that run starts and
// returns its sockets as listeners, all or nothing. run gets the helper's
// arguments, ListenHelperArg first, and returns once the helper exited.
// An address the helper could not bind comes back as a *BindError, like
// Listen's; a helper that handed over nothing as a *HandoffError.
func ListenThrough(ctx context.Context, routes []Route, run func(ctx context.Context, args []string) error) ([]net.Listener, error) {
	dir, err := os.MkdirTemp("", "skali-listen-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	socketPath := filepath.Join(dir, "s")
	server, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	defer server.Close()

	args := []string{ListenHelperArg, socketPath}
	for _, route := range routes {
		args = append(args, route.Listen)
	}
	runErr := run(ctx, args)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// The helper connects and writes before it exits, so its message is
	// queued by now, or it never sent one.
	_ = server.SetDeadline(time.Now().Add(acceptWait))
	conn, err := server.AcceptUnix()
	if err != nil {
		if runErr == nil {
			runErr = errNoHandoff
		}
		return nil, &HandoffError{Err: runErr}
	}
	defer conn.Close()
	return receiveListeners(conn, routes)
}

func receiveListeners(conn *net.UnixConn, routes []Route) ([]net.Listener, error) {
	_ = conn.SetReadDeadline(time.Now().Add(readWait))
	payload := make([]byte, 4096)
	rights := make([]byte, syscall.CmsgSpace(maxHelperAddresses*4))
	n, rightsLen, _, _, err := conn.ReadMsgUnix(payload, rights)
	if err != nil {
		return nil, &HandoffError{Err: fmt.Errorf("read the listen helper's sockets: %w", err)}
	}
	files, filesErr := receivedFiles(rights[:rightsLen])
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	if filesErr != nil {
		return nil, &HandoffError{Err: filesErr}
	}
	var reply handoffReply
	if err := json.Unmarshal(payload[:n], &reply); err != nil {
		return nil, &HandoffError{Err: fmt.Errorf("read the listen helper's reply: %w", err)}
	}
	if reply.Error != "" {
		return nil, replyError(reply, routes)
	}
	return FileListeners(routes, files)
}

// receivedFiles opens the descriptors a message carried, close-on-exec:
// a session must not leak its listeners into the processes it starts.
func receivedFiles(rights []byte) ([]*os.File, error) {
	messages, err := syscall.ParseSocketControlMessage(rights)
	if err != nil {
		return nil, fmt.Errorf("read the listen helper's sockets: %w", err)
	}
	var files []*os.File
	for index := range messages {
		descriptors, err := syscall.ParseUnixRights(&messages[index])
		if err != nil {
			continue
		}
		for _, descriptor := range descriptors {
			syscall.CloseOnExec(descriptor)
			files = append(files, os.NewFile(uintptr(descriptor), "listener"))
		}
	}
	return files, nil
}

// replyError is the helper's bind failure as Listen would have reported it.
func replyError(reply handoffReply, routes []Route) error {
	for _, route := range routes {
		if route.Listen != reply.Listen {
			continue
		}
		if reply.Errno != 0 {
			return &BindError{Route: route, Err: syscall.Errno(reply.Errno)}
		}
		return &BindError{Route: route, Err: errors.New(reply.Error)}
	}
	return errors.New(reply.Error)
}

// ListenHelper is the helper's side: bind every address, then connect to
// socketPath and pass the sockets, or the first address that failed. It
// runs as root, so it takes nothing but IP addresses with ports, never the
// wildcard, and touches no file but the socket.
func ListenHelper(socketPath string, addresses []string) error {
	if len(addresses) == 0 || len(addresses) > maxHelperAddresses {
		return fmt.Errorf("pass between 1 and %d addresses to listen on", maxHelperAddresses)
	}
	for _, address := range addresses {
		addrPort, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%q is not an IP address with a port", address)
		}
		if addrPort.Addr().WithZone("").IsUnspecified() || addrPort.Port() == 0 {
			return fmt.Errorf("refusing to listen on %s: name one address and port", address)
		}
	}
	var reply handoffReply
	var files []*os.File
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	for _, address := range addresses {
		file, err := listenFile(address)
		if err != nil {
			reply = handoffReply{Error: err.Error(), Listen: address}
			if errno, ok := errors.AsType[syscall.Errno](err); ok {
				reply.Errno = int(errno)
			}
			break
		}
		files = append(files, file)
	}
	var rights []byte
	if reply.Error == "" {
		descriptors := make([]int, 0, len(files))
		for _, file := range files {
			descriptors = append(descriptors, int(file.Fd()))
		}
		rights = syscall.UnixRights(descriptors...)
	}
	payload, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, _, err = conn.WriteMsgUnix(payload, rights, nil)
	return err
}

// listenFile binds address and returns the listening socket as a file.
func listenFile(address string) (*os.File, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	defer listener.Close()
	return listener.(*net.TCPListener).File()
}
