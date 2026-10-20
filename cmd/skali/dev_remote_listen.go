package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/Hinkolas/skali/internal/devremote"
)

// remoteListenOS is the system whose rules for ports below 1024 apply; a
// variable so tests can take either path.
var remoteListenOS = runtime.GOOS

// listenDirect binds as this process; a variable so tests can make it
// meet macOS's refusal on any system.
var listenDirect = devremote.Listen

// listenRoutes binds a proxy or expose session's routes. A background
// session takes the sockets the process that started it bound. On macOS,
// which lets only root listen below port 1024 of a specific address,
// sudo runs the listen helper to bind them; the session itself stays this
// user's process. Linux keeps the refusal, which explains its sysctl.
func listenRoutes(ctx context.Context, routes []devremote.Route, command string) ([]net.Listener, error) {
	if listeners, inherited, err := inheritedListeners(routes); inherited {
		return listeners, err
	}
	listeners, err := listenDirect(routes)
	if err == nil || remoteListenOS != "darwin" || !errors.Is(err, syscall.EACCES) {
		return listeners, err
	}
	// macOS refuses the user before it looks for another listener, so a
	// port in use, such as the local platform's edge, would only show
	// after the password; a connection that is answered shows it first.
	for _, route := range routes {
		if edgeAnswers(route.Listen) {
			return nil, &devremote.BindError{Route: route, Err: syscall.EADDRINUSE}
		}
	}
	return listenAsAdministrator(ctx, routes, command)
}

// openPasswordTerminal opens the terminal sudo asks for the password on:
// the controlling terminal, whatever stdin and stdout are redirected to. A
// background session has none. A variable for tests.
var openPasswordTerminal = func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_WRONLY, 0) }

func listenAsAdministrator(ctx context.Context, routes []devremote.Route, command string) ([]net.Listener, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	addresses := routeAddresses(routes)
	// The explanation goes where the prompt appears, so it stays next to it
	// when the output goes to a file.
	terminal, err := openPasswordTerminal()
	interactive := err == nil
	if interactive {
		fmt.Fprintf(terminal, "macOS lets only administrators listen on %s. sudo opens them for %s, "+
			"which keeps running as you; it may ask for your login password.\n", addresses, command)
		terminal.Close()
	}
	// Ctrl-C at the password prompt, or closing the terminal, ends sudo as
	// well; wait for it to exit so the helper's socket directory is
	// removed.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	listeners, err := devremote.ListenThrough(ctx, routes, func(ctx context.Context, args []string) error {
		return runAsAdministrator(ctx, interactive, executable, args)
	})
	if err == nil {
		return listeners, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s stopped before sudo opened %s", command, addresses)
	}
	handoff, ok := errors.AsType[*devremote.HandoffError](err)
	if !ok {
		return nil, err
	}
	if !interactive {
		return nil, fmt.Errorf("macOS lets only administrators listen on %s, and sudo could not open them without a terminal "+
			"to ask for your password (%v); run %s in a terminal", addresses, handoff.Err, command)
	}
	return nil, fmt.Errorf("macOS lets only administrators listen on %s, and sudo did not open them (%v); "+
		"run %s again and enter your login password, as a user who is an administrator of this Mac", addresses, handoff.Err, command)
}

// runAsAdministrator runs this binary with args as root through sudo,
// which asks for the password on the controlling terminal when there is
// one and fails at once when there is none. The error carries sudo's last line, such as
// its incorrect-password or not-in-sudoers message.
var runAsAdministrator = func(ctx context.Context, interactive bool, executable string, args []string) error {
	sudoArgs := []string{"-n"}
	if interactive {
		sudoArgs = nil
	}
	sudoArgs = append(append(sudoArgs, "--", executable), args...)
	command := exec.CommandContext(ctx, "/usr/bin/sudo", sudoArgs...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if interactive {
		command.Stderr = io.MultiWriter(os.Stderr, &output)
	}
	err := command.Run()
	if err == nil {
		return nil
	}
	if lines := strings.Split(strings.TrimSpace(output.String()), "\n"); lines[len(lines)-1] != "" {
		return errors.New(strings.TrimSpace(lines[len(lines)-1]))
	}
	return fmt.Errorf("sudo: %w", err)
}

// runListenHelper is the skali binary run as the listen helper: it binds
// the addresses it is given and passes them to the socket that follows
// ListenHelperArg.
func runListenHelper(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: skali "+devremote.ListenHelperArg+" <socket> <address:port>...")
		return 2
	}
	if err := devremote.ListenHelper(args[0], args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "skali listen helper: "+err.Error())
		return 1
	}
	return 0
}

// routeAddresses names where routes listen: 127.0.0.1:443 and
// 127.0.0.1:80.
func routeAddresses(routes []devremote.Route) string {
	addresses := make([]string, 0, len(routes))
	for _, route := range routes {
		addresses = append(addresses, route.Listen)
	}
	return strings.Join(addresses, " and ")
}

// privilegedPortError explains a refused listen below port 1024 in the
// terms of this system.
func privilegedPortError(port, command string) error {
	switch remoteListenOS {
	case "linux":
		return fmt.Errorf("listening on port %s needs privileges on this system; allow unprivileged web ports with "+
			"sudo sysctl -w net.ipv4.ip_unprivileged_port_start=80 (a file in /etc/sysctl.d keeps it) and run %s again", port, command)
	case "darwin":
		return fmt.Errorf("macOS lets only administrators listen on port %s of a specific address; "+
			"run %s in a terminal as an administrator of this Mac, and sudo asks for your password to open it", port, command)
	}
	return fmt.Errorf("listening on port %s needs privileges on this system; run %s with them", port, command)
}
