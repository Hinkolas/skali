//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/devremote"
)

// onMacOS makes listenRoutes meet macOS's refusal to bind below port 1024
// as a user, whatever the system and ports the test runs on, and runs the
// listen helper in this process where sudo would run it as root. There is
// no controlling terminal unless the test sets one up with withTerminal.
func onMacOS(t *testing.T, sudo func(args []string) error) (sudoRuns *int) {
	t.Helper()
	previousOS, previousListen, previousSudo, previousTerminal := remoteListenOS, listenDirect, runAsAdministrator, openPasswordTerminal
	t.Cleanup(func() {
		remoteListenOS, listenDirect, runAsAdministrator, openPasswordTerminal = previousOS, previousListen, previousSudo, previousTerminal
	})
	remoteListenOS = "darwin"
	openPasswordTerminal = func() (*os.File, error) { return nil, errors.New("no controlling terminal") }
	listenDirect = func(routes []devremote.Route) ([]net.Listener, error) {
		return nil, &devremote.BindError{Route: routes[0], Err: &net.OpError{Op: "listen", Err: syscall.EACCES}}
	}
	sudoRuns = new(int)
	runAsAdministrator = func(ctx context.Context, interactive bool, executable string, args []string) error {
		*sudoRuns++
		terminal, terminalErr := openPasswordTerminal()
		if terminalErr == nil {
			terminal.Close()
		}
		require.Equal(t, terminalErr == nil, interactive, "sudo prompts exactly when there is a terminal to prompt on")
		require.NotEmpty(t, executable)
		require.Equal(t, devremote.ListenHelperArg, args[0])
		return sudo(args)
	}
	return sudoRuns
}

// withTerminal gives onMacOS a controlling terminal, a file standing in for
// /dev/tty, and returns its path.
func withTerminal(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tty")
	openPasswordTerminal = func() (*os.File, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	return path
}

func helperInProcess(args []string) error { return devremote.ListenHelper(args[1], args[2:]) }

func loopbackRoutes(t *testing.T) []devremote.Route {
	t.Helper()
	return []devremote.Route{
		{Scheme: "https", Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort(t)))},
		{Scheme: "http", Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort(t)))},
	}
}

func TestListenRoutesOpensPrivilegedPortsThroughSudoOnMacOS(t *testing.T) {
	sudoRuns := onMacOS(t, helperInProcess)
	routes := loopbackRoutes(t)
	listeners, err := listenRoutes(t.Context(), routes, "skali dev proxy")
	require.NoError(t, err)
	defer closeListeners(listeners)
	require.Equal(t, 1, *sudoRuns)
	for index, listener := range listeners {
		require.Equal(t, routes[index].Listen, listener.Addr().String())
	}
}

// sudo asks on the controlling terminal, so output redirected to a file
// does not keep it from asking, and the explanation lands next to the
// prompt rather than in the file.
func TestListenRoutesAsksOnTheControllingTerminal(t *testing.T) {
	onMacOS(t, func([]string) error { return errors.New("sudo: 3 incorrect password attempts") })
	terminal := withTerminal(t)
	_, err := listenRoutes(t.Context(), loopbackRoutes(t), "skali dev proxy")
	require.ErrorContains(t, err, "sudo did not open them (sudo: 3 incorrect password attempts)")
	require.ErrorContains(t, err, "run skali dev proxy again and enter your login password")
	shown, readErr := os.ReadFile(terminal)
	require.NoError(t, readErr)
	require.Contains(t, string(shown), "sudo opens them for skali dev proxy, which keeps running as you")
}

func TestListenRoutesExplainsADeniedSudoOnMacOS(t *testing.T) {
	onMacOS(t, func([]string) error { return errors.New("sudo: a password is required") })
	_, err := listenRoutes(t.Context(), loopbackRoutes(t), "skali dev proxy")
	require.ErrorContains(t, err, "macOS lets only administrators listen on 127.0.0.1:")
	require.ErrorContains(t, err, "sudo: a password is required")
	require.ErrorContains(t, err, "run skali dev proxy in a terminal")
	require.NotContains(t, err.Error(), "sysctl")
}

// A port something already answers on is in use: asking for the password
// first would only end in that.
func TestListenRoutesNamesAPortInUseBeforeAskingSudo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sudoRuns := onMacOS(t, helperInProcess)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()
	routes := append(loopbackRoutes(t)[:1], devremote.Route{Scheme: "http", Listen: occupied.Addr().String()})

	_, err = listenRoutes(t.Context(), routes, "skali dev proxy")
	require.ErrorIs(t, err, syscall.EADDRINUSE)
	require.Zero(t, *sudoRuns)
	bind, ok := errors.AsType[*devremote.BindError](err)
	require.True(t, ok)
	require.Equal(t, routes[1], bind.Route)
	_, port, _ := net.SplitHostPort(occupied.Addr().String())
	require.ErrorContains(t, explainProxyBind(err), "port "+port+" is in use on this machine")
}

// Linux has no helper: the refusal stands and explains the sysctl.
func TestListenRoutesLeavesLinuxToItsSysctl(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sudoRuns := onMacOS(t, helperInProcess)
	remoteListenOS = "linux"
	_, err := listenRoutes(t.Context(), loopbackRoutes(t), "skali dev proxy")
	require.ErrorIs(t, err, syscall.EACCES)
	require.Zero(t, *sudoRuns)
	require.ErrorContains(t, explainProxyBind(err), "net.ipv4.ip_unprivileged_port_start=80")
}

// A background session serves on the sockets its parent bound, and never
// asks sudo itself: it has no terminal to ask on.
func TestListenRoutesTakesTheSocketsABackgroundSessionInherits(t *testing.T) {
	sudoRuns := onMacOS(t, helperInProcess)
	routes := loopbackRoutes(t)
	bound, err := devremote.Listen(routes)
	require.NoError(t, err)
	defer closeListeners(bound)
	files, err := listenerFiles(bound)
	require.NoError(t, err)
	descriptors := ""
	for index, file := range files {
		// The inheriting side owns what it was handed: give it its own
		// descriptors, as exec would.
		duplicate, err := syscall.Dup(int(file.Fd()))
		require.NoError(t, err)
		file.Close()
		if index > 0 {
			descriptors += ","
		}
		descriptors += strconv.Itoa(duplicate)
	}
	t.Setenv(envListenFDs, descriptors)

	listeners, err := listenRoutes(t.Context(), routes, "skali dev proxy")
	require.NoError(t, err)
	defer closeListeners(listeners)
	require.Zero(t, *sudoRuns)
	require.Len(t, listeners, 2)
	_, set := os.LookupEnv(envListenFDs)
	require.False(t, set, "the processes the session starts inherit nothing")

	t.Setenv(envListenFDs, "nope")
	_, err = listenRoutes(t.Context(), routes, "skali dev proxy")
	require.ErrorContains(t, err, "does not name inherited sockets")
}

func TestPrivilegedPortErrorNamesTheSystemsFix(t *testing.T) {
	previous := remoteListenOS
	t.Cleanup(func() { remoteListenOS = previous })
	remoteListenOS = "linux"
	require.ErrorContains(t, privilegedPortError("443", "skali dev expose"), "sudo sysctl -w net.ipv4.ip_unprivileged_port_start=80")
	remoteListenOS = "darwin"
	err := privilegedPortError("443", "skali dev expose")
	require.ErrorContains(t, err, "macOS lets only administrators listen on port 443")
	require.ErrorContains(t, err, "run skali dev expose in a terminal")
	require.NotContains(t, err.Error(), "sysctl")
}

func TestRunListenHelperNeedsASocketAndAddresses(t *testing.T) {
	require.Equal(t, 2, runListenHelper(nil))
	require.Equal(t, 2, runListenHelper([]string{t.TempDir() + "/s"}))
	require.Equal(t, 1, runListenHelper([]string{t.TempDir() + "/s", "0.0.0.0:443"}))
}
