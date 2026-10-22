package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
)

// execInvocation carries the flag surface shared by `skali exec` and
// `skali dev exec`.
type execInvocation struct {
	forceTTY  bool
	noTTY     bool
	noStdin   bool
	pod       string
	container string
	timeout   time.Duration
}

func addExecFlags(command *cobra.Command, inv *execInvocation) {
	command.Flags().BoolVarP(&inv.forceTTY, "tty", "t", false,
		"allocate a remote TTY even when stdout is not a terminal")
	command.Flags().BoolVar(&inv.noTTY, "no-tty", false,
		"never allocate a remote TTY")
	command.Flags().BoolVarP(&inv.noStdin, "no-stdin", "n", false,
		"do not forward stdin: the command sees end of input at once (like ssh -n); implies --no-tty")
	command.Flags().StringVar(&inv.pod, "pod", "",
		"exec into this pod instead of the newest ready one")
	command.Flags().StringVar(&inv.container, "container", "",
		"container name (default: the service's own container)")
	addSessionTimeoutFlag(command, &inv.timeout)
}

// addSessionTimeoutFlag is the wall-clock bound every session command
// shares.
func addSessionTimeoutFlag(command *cobra.Command, timeout *time.Duration) {
	command.Flags().DurationVar(timeout, "timeout", 0,
		"end the session after this long, such as 30s or 5m, and exit with status 124 (default: no limit)")
}

func newExecCommand() *cobra.Command {
	var environment, remote string
	var inv execInvocation
	command := &cobra.Command{
		Use:   "exec [service] [flags] -- <command>...",
		Short: "Run a command in a running app container",
		Long: "Exec opens a command or interactive shell inside a running container of\n" +
			"one service, like docker exec. Without a command it starts /bin/sh.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeServiceArg,
		RunE: func(command *cobra.Command, args []string) error {
			service, argv, err := parseExecArgs(command, args)
			if err != nil {
				return err
			}
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(command.Context(), start, environment, remote)
			if err != nil {
				return err
			}
			if service == "" && inv.pod == "" {
				if service, err = defaultExecService(command); err != nil {
					return err
				}
			}
			reauth := sessionReauth(command, target.api, target.remoteName)
			prompt := shellPrompt(target.remoteName, service, inv, target.project+"/"+target.environment)
			return runExecSession(command, target.api, target.environmentID, inv, service, argv, prompt, reauth)
		},
	}
	command.Flags().StringVar(&environment, "environment", "",
		"environment name (default: the checkout binding)")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	addExecFlags(command, &inv)
	return command
}

// parseExecArgs splits "exec [service] -- <command>..." on the dash
// separator: at most one positional service before it, the remote command
// vector after it.
func parseExecArgs(command *cobra.Command, args []string) (service string, argv []string, err error) {
	dash := command.ArgsLenAtDash()
	positional := args
	if dash >= 0 {
		positional, argv = args[:dash], args[dash:]
	}
	switch len(positional) {
	case 0:
	case 1:
		service = positional[0]
	default:
		return "", nil, fmt.Errorf("separate the command with --: %s [service] -- <command>...", command.CommandPath())
	}
	return service, argv, nil
}

// defaultExecService picks the service when none was named: the manifest's
// sole application, or an interactive choice among several.
func defaultExecService(command *cobra.Command) (string, error) {
	project, err := readLocalProject("", nil)
	if err != nil {
		return "", errors.New("no service named and no manifest found; name the service explicitly")
	}
	keys := slices.Sorted(maps.Keys(project.Result.Definition.Applications))
	switch len(keys) {
	case 0:
		return "", errors.New("the manifest declares no applications")
	case 1:
		return keys[0], nil
	}
	if !cliprompt.Interactive() {
		return "", fmt.Errorf("several applications in the manifest; name one of: %s", strings.Join(keys, ", "))
	}
	session := promptSession(command.OutOrStdout(), bufio.NewReader(command.InOrStdin()))
	options := make([]cliprompt.Option, 0, len(keys))
	for _, key := range keys {
		options = append(options, cliprompt.Option{Label: key, Value: key})
	}
	return session.Select(command.Context(), cliprompt.SelectOptions{Title: "Service", Options: options})
}

// shellPrompt is the PS1 seeded into the default interactive shell so
// every session names where it runs: (remote) service@scope cwd $. The
// \w escape renders the working directory in busybox ash and bash;
// shells without prompt escapes (plain dash) print it literally.
func shellPrompt(remote, service string, inv execInvocation, scope string) string {
	target := service
	if target == "" {
		target = inv.pod
	}
	return fmt.Sprintf("(%s) %s@%s \\w $ ", remote, target, scope)
}

// decideTTY resolves whether the session allocates a remote TTY: an
// explicit flag wins, otherwise a TTY is allocated exactly when stdin and
// stdout are both terminals.
func decideTTY(inv execInvocation) (bool, error) {
	if inv.noStdin && inv.forceTTY {
		return false, errors.New("--tty needs stdin; drop --no-stdin")
	}
	if inv.noTTY || inv.noStdin {
		return false, nil
	}
	stdinTerminal := clirender.IsTerminal(os.Stdin)
	if inv.forceTTY {
		if !stdinTerminal {
			return false, errors.New("the input device is not a TTY")
		}
		return true, nil
	}
	return stdinTerminal && clirender.IsTerminal(os.Stdout), nil
}

// runExecSession opens the exec session and drives it with runSession.
func runExecSession(command *cobra.Command, api *client.Client, environmentID string,
	inv execInvocation, service string, argv []string, prompt string, reauth func(context.Context) error) error {
	tty, err := decideTTY(inv)
	if err != nil {
		return err
	}
	opts := client.ExecOptions{
		Service:   service,
		Pod:       inv.pod,
		Container: inv.container,
		Command:   argv,
		TTY:       tty,
	}
	if tty && len(argv) == 0 && prompt != "" {
		// The exec subresource cannot seed environment variables, so the
		// default interactive shell starts through a wrapper that exports
		// PS1. The prompt rides as a positional argument, so no shell
		// quoting applies to it.
		opts.Command = []string{"/bin/sh", "-c", `export PS1="$1"; exec /bin/sh`, "sh", prompt}
	}
	var stdin io.Reader = os.Stdin
	if inv.noStdin {
		stdin = strings.NewReader("")
	}
	return runSession(command.Context(), sessionDrive{tty: tty, stdin: stdin, timeout: inv.timeout},
		func(ctx context.Context) (*client.ExecSession, error) { return api.Exec(ctx, environmentID, opts) },
		reauth)
}

// sessionDrive is how runSession attaches the local process to a session.
type sessionDrive struct {
	tty   bool
	stdin io.Reader // forwarded until EOF, which half-closes the remote stdin
	// timeout bounds the whole session, opening included; zero is none.
	timeout time.Duration
}

// cancelGrace is how long a cancelled session may take to wind down
// before runSession reports the cancellation without it.
var cancelGrace = 3 * time.Second

// errSessionTimeout is the cancellation cause of an expired --timeout.
var errSessionTimeout = errors.New("session timeout")

// interruptCause records which signal ended a session.
type interruptCause struct{ signal os.Signal }

func (c interruptCause) Error() string { return "interrupted by " + c.signal.String() }

// runSession opens a session (exec or SQL) with one reauth retry, like
// every other sudo-gated command, owns the local terminal for its
// duration, and maps the outcome onto the process's exit status: the
// remote status passes through, a local interrupt exits 128+signal and an
// expired --timeout 124 (the conventions of shells and timeout(1)), so an
// interrupted or cut-off session never reads as success.
func runSession(parent context.Context, drive sessionDrive,
	open func(context.Context) (*client.ExecSession, error), reauth func(context.Context) error) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	if drive.timeout > 0 {
		var cancelTimeout context.CancelFunc
		ctx, cancelTimeout = context.WithTimeoutCause(ctx, drive.timeout, errSessionTimeout)
		defer cancelTimeout()
	}
	// Interrupts end the session (the server kills the remote process
	// when the connection goes). In raw mode Ctrl-C is a keystroke for
	// the remote process instead and raises no signal here.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case sig := <-signals:
			cancel(interruptCause{signal: sig})
		case <-ctx.Done():
		}
	}()

	// A cancelled session normally returns at once, but a write to a local
	// stdout nobody drains (a pipe reader that stopped reading) blocks in
	// the kernel where no context reaches it. Past a short grace the
	// outcome stands without it; the terminal is restored here since the
	// stuck call never gets to.
	var terminal *term.State
	if drive.tty {
		terminal, _ = term.GetState(int(os.Stdin.Fd()))
	}
	result := make(chan error, 1)
	go func() { result <- driveSession(ctx, drive, open, reauth) }()
	var err error
	select {
	case err = <-result:
	case <-ctx.Done():
		select {
		case err = <-result:
		case <-time.After(cancelGrace):
			if terminal != nil {
				_ = term.Restore(int(os.Stdin.Fd()), terminal)
			}
			err = ctx.Err()
		}
	}
	if _, remote := errors.AsType[*client.ExecExitError](err); err == nil || remote || ctx.Err() == nil {
		return err
	}
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errSessionTimeout):
		return &codedError{err: fmt.Errorf("session timed out after %s", drive.timeout), code: 124}
	default:
		if interrupt, ok := errors.AsType[interruptCause](cause); ok {
			code := 130
			if sig, ok := interrupt.signal.(syscall.Signal); ok {
				code = 128 + int(sig)
			}
			return &client.ExecExitError{Code: code}
		}
	}
	return err
}

func driveSession(ctx context.Context, drive sessionDrive,
	open func(context.Context) (*client.ExecSession, error), reauth func(context.Context) error) error {
	session, err := open(ctx)
	if isReauthRequired(err) {
		if err = reauth(ctx); err != nil {
			return err
		}
		session, err = open(ctx)
	}
	if err != nil {
		return err
	}
	defer session.Close()

	if !drive.tty {
		// Plain pipe mode: stdin EOF propagates as a half-close.
		return session.Run(ctx, drive.stdin, os.Stdout, os.Stderr, nil)
	}

	// Raw mode for the session's duration: keystrokes (including Ctrl-C)
	// pass through to the remote process; the session ends when the remote
	// command exits (Ctrl-D or exit for shells).
	stdinFd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("enable raw terminal mode: %w", err)
	}
	defer func() { _ = term.Restore(stdinFd, state) }()

	resize := make(chan client.TerminalSize, 4)
	if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		resize <- client.TerminalSize{Cols: uint16(cols), Rows: uint16(rows)}
	}
	stopResize := watchResize(resize)
	defer stopResize()

	return session.Run(ctx, drive.stdin, os.Stdout, os.Stderr, resize)
}

// untilDone runs call but returns as soon as ctx ends: the plain password
// prompt blocks in a stdin read that no context reaches, and runSession
// holds SIGINT, so without this neither Ctrl-C nor --timeout would end a
// session waiting for an answer. The abandoned read ends with the process.
func untilDone(ctx context.Context, call func() error) error {
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// codedError is a failure with a conventional exit status of its own; the
// error line still prints.
type codedError struct {
	err  error
	code int
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

// sessionReauth confirms identity for a session command without ever
// reading a piped stdin: that stream is the remote process's input (a SQL
// script, a file for cat), and an answer read from it would both consume
// it and treat it as a password. With a terminal on stdin it prompts like
// any command (on stderr, so a redirected stdout stays clean); otherwise
// only the local platform's own admin confirms unattended, and everyone
// else is told how to get a fresh session first.
func sessionReauth(command *cobra.Command, api *client.Client, remoteName string) func(context.Context) error {
	return func(ctx context.Context) error {
		if clirender.IsTerminal(os.Stdin) {
			// A password prompt turns echo off for its read; when the
			// session ends while it waits, the read never returns to turn
			// it back on, so the state from before the prompt is restored
			// here.
			fd := int(os.Stdin.Fd())
			state, stateErr := term.GetState(fd)
			err := untilDone(ctx, func() error {
				return reauthSession(ctx, command.ErrOrStderr(), bufio.NewReader(os.Stdin), api)
			})
			if ctx.Err() != nil && stateErr == nil {
				_ = term.Restore(fd, state)
			}
			return err
		}
		local, err := isLocalAdmin(ctx, api)
		if err != nil {
			return err
		}
		if local {
			return reauthLocal(ctx, api)
		}
		return fmt.Errorf("recent authentication required, and skali does not prompt while stdin carries the session's input: "+
			"confirm in a terminal first (skali remote login %s), then run this again", remoteName)
	}
}

// isNoReadyPod matches the exec endpoint's refusal when the service has no
// ready pod to exec into.
func isNoReadyPod(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Code == "no_ready_pod"
}
