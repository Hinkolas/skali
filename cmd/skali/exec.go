package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

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
	pod       string
	container string
}

func addExecFlags(command *cobra.Command, inv *execInvocation) {
	command.Flags().BoolVarP(&inv.forceTTY, "tty", "t", false,
		"allocate a remote TTY even when stdout is not a terminal")
	command.Flags().BoolVar(&inv.noTTY, "no-tty", false,
		"never allocate a remote TTY")
	command.Flags().StringVar(&inv.pod, "pod", "",
		"exec into this pod instead of the newest ready one")
	command.Flags().StringVar(&inv.container, "container", "",
		"container name (default: the service's own container)")
}

func newExecCommand() *cobra.Command {
	var environment, remote string
	var inv execInvocation
	command := &cobra.Command{
		Use:   "exec [service] [flags] -- <command>...",
		Short: "Run a command in a running app container",
		Long: "Exec opens a command or interactive shell inside a running container of\n" +
			"one service, like docker exec. Without a command it starts /bin/sh.",
		Args: cobra.ArbitraryArgs,
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
			reauth := func(ctx context.Context) error {
				return reauthForAdmin(ctx, command, command.OutOrStdout(), target.api)
			}
			scope := target.environment
			if target.project != "" {
				scope = target.project + "/" + target.environment
			}
			prompt := shellPrompt(target.remoteName, service, inv, scope)
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
	project, err := loadLocalProject("")
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
	if inv.noTTY {
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

// runExecSession opens the exec session (with one reauth retry, like every
// other sudo-gated command), owns the local terminal for its duration, and
// maps the remote exit status onto the process's own.
func runExecSession(command *cobra.Command, api *client.Client, environmentID string,
	inv execInvocation, service string, argv []string, prompt string, reauth func(context.Context) error) error {
	ctx := command.Context()
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
	session, err := api.Exec(ctx, environmentID, opts)
	if isReauthRequired(err) {
		if err = reauth(ctx); err != nil {
			return err
		}
		session, err = api.Exec(ctx, environmentID, opts)
	}
	if err != nil {
		return err
	}
	defer session.Close()

	if !tty {
		// Plain pipe mode: local interrupts cancel the session (the server
		// kills the remote exec); stdin EOF propagates as a half-close.
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := session.Run(runCtx, os.Stdin, os.Stdout, os.Stderr, nil)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
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

	return session.Run(ctx, os.Stdin, os.Stdout, os.Stderr, resize)
}

// isNoReadyPod matches the exec endpoint's refusal when the service has no
// ready pod to exec into.
func isNoReadyPod(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Code == "no_ready_pod"
}
