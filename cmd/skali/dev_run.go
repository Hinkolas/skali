package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/utils"
)

func newDevRunCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "run [app] [name] -- <command>...",
		Short: "Run a command on the host with an application's resolved environment",
		Long: "Runs a named command from the manifest (applications.<app>.commands)\n" +
			"or a raw command after --, on this machine in the project root, with\n" +
			"the application's fully resolved environment: stored values plus\n" +
			"database and bucket outputs rewritten to the local platform's\n" +
			"loopback ports. Seeds and migrations against the dev database live\n" +
			"here. Bare skali dev run lists the declared commands, skali dev run\n" +
			"<app> those of one application. For a shell inside the running\n" +
			"container, see skali dev exec; skali run manages journal runs, not\n" +
			"project commands.",
		Args: cobra.ArbitraryArgs,
		RunE: runDevRun,
	}
}

func runDevRun(command *cobra.Command, args []string) error {
	project, err := loadLocalProject("")
	if err != nil {
		return err
	}
	if listApp, list := devRunListTarget(command, args, project); list {
		renderDevCommands(command.OutOrStdout(), project, listApp, command.CommandPath())
		return nil
	}
	appKey, argv, err := parseDevRunArgs(command, args, project)
	if err != nil {
		return err
	}
	api, environmentID, err := localProjectEnvironment(command)
	if err != nil {
		return fmt.Errorf("%w; start a session with skali dev first", err)
	}
	ctx := command.Context()
	resolved, err := api.ApplicationEnvironment(ctx, environmentID, appKey, localdev.LoopbackPortBase())
	if isReauthRequired(err) {
		if err = reauthLocal(ctx, api); err == nil {
			resolved, err = api.ApplicationEnvironment(ctx, environmentID, appKey, localdev.LoopbackPortBase())
		}
	}
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 409) {
			return fmt.Errorf("%w; deploy first with skali dev", err)
		}
		return err
	}
	stderr := command.ErrOrStderr()
	style := clirender.StyleFor(stderr)
	for _, warning := range resolved.Warnings {
		fmt.Fprintf(stderr, "%s\n", style.Yellow("warning: "+warning))
	}

	child := exec.CommandContext(ctx, argv[0], argv[1:]...)
	child.Dir = project.Root
	environ, _ := devChildEnviron(resolved.Values, nil)
	child.Env = environ
	// One-shot commands own the terminal directly: interactive tools work,
	// and Ctrl-C reaches the child through the shared foreground process
	// group while the CLI just waits for the exit.
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	if err := child.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			// The command ran and reported its own status: pass it through
			// silently, exactly like a remote exec exit.
			return &client.ExecExitError{Code: exitErr.ExitCode()}
		}
		return fmt.Errorf("run %s: %w", argv[0], err)
	}
	return nil
}

// parseDevRunArgs resolves the run target: a raw argv after --, a bare
// command name matched across every application, or app plus name.
func parseDevRunArgs(command *cobra.Command, args []string, project *localProject) (string, []string, error) {
	dash := command.ArgsLenAtDash()
	positional := args
	var argv []string
	if dash >= 0 {
		positional, argv = args[:dash], args[dash:]
	}
	if len(argv) > 0 {
		switch len(positional) {
		case 0:
			appKey, err := defaultExecService(command)
			return appKey, argv, err
		case 1:
			if _, ok := project.Document.Project.Applications[positional[0]]; !ok {
				return "", nil, fmt.Errorf("unknown application %q", positional[0])
			}
			return positional[0], argv, nil
		default:
			return "", nil, fmt.Errorf("separate the command with --: %s [app] -- <command>...", command.CommandPath())
		}
	}
	switch len(positional) {
	case 1:
		return findNamedCommand(project, positional[0], command.CommandPath())
	case 2:
		application, ok := project.Document.Project.Applications[positional[0]]
		if !ok {
			return "", nil, fmt.Errorf("unknown application %q", positional[0])
		}
		named, ok := application.Commands[positional[1]]
		if !ok {
			return "", nil, fmt.Errorf("application %s declares no command %q%s",
				positional[0], positional[1], availableSuffix(application.Commands))
		}
		return positional[0], named, nil
	default:
		return "", nil, fmt.Errorf("usage: %[1]s <name>, %[1]s <app> <name>, or %[1]s [app] -- <command>...",
			command.CommandPath())
	}
}

// findNamedCommand resolves a bare command name across every application:
// exactly one owner runs it, several are ambiguous, none lists what exists.
func findNamedCommand(project *localProject, name, commandPath string) (string, []string, error) {
	var owners []string
	for key, application := range project.Document.Project.Applications {
		if _, ok := application.Commands[name]; ok {
			owners = append(owners, key)
		}
	}
	sort.Strings(owners)
	switch len(owners) {
	case 1:
		return owners[0], project.Document.Project.Applications[owners[0]].Commands[name], nil
	case 0:
		var known []string
		for _, key := range utils.SortedKeys(project.Document.Project.Applications) {
			for _, commandKey := range utils.SortedKeys(project.Document.Project.Applications[key].Commands) {
				known = append(known, key+" "+commandKey)
			}
		}
		if len(known) == 0 {
			return "", nil, fmt.Errorf("no application declares a command %q; add it under applications.<app>.commands", name)
		}
		return "", nil, fmt.Errorf("no application declares a command %q; declared: %s", name, strings.Join(known, ", "))
	default:
		return "", nil, fmt.Errorf("command %q is declared by %s; name the application: %s %s %s",
			name, strings.Join(owners, " and "), commandPath, owners[0], name)
	}
}

func availableSuffix(commands map[string][]string) string {
	if len(commands) == 0 {
		return ""
	}
	return "; declared: " + strings.Join(utils.SortedKeys(commands), ", ")
}

// devRunListTarget decides whether an invocation asks for the command
// listing instead of a run: no arguments at all, or a single application
// name that no application also uses as a command name. The second form
// lists that application's commands only. Refs #21.
func devRunListTarget(command *cobra.Command, args []string, project *localProject) (string, bool) {
	if command.ArgsLenAtDash() >= 0 {
		return "", false
	}
	switch len(args) {
	case 0:
		return "", true
	case 1:
		if _, ok := project.Document.Project.Applications[args[0]]; !ok {
			return "", false
		}
		for _, application := range project.Document.Project.Applications {
			if _, ok := application.Commands[args[0]]; ok {
				return "", false
			}
		}
		return args[0], true
	}
	return "", false
}

// renderDevCommands prints the declared commands grouped by application,
// the way bun run lists package scripts: name, then the command it runs.
// appKey narrows the listing to one application.
func renderDevCommands(out io.Writer, project *localProject, appKey, commandPath string) {
	style := clirender.StyleFor(out)
	applications := project.Document.Project.Applications
	keys := utils.SortedKeys(applications)
	if appKey != "" {
		keys = []string{appKey}
	}
	keys = slices.DeleteFunc(keys, func(key string) bool { return len(applications[key].Commands) == 0 })
	manifest := filepath.Base(project.Path)
	if len(keys) == 0 {
		if appKey != "" {
			fmt.Fprintf(out, "%s\n", style.Dim(fmt.Sprintf("application %s declares no commands in %s", appKey, manifest)))
			fmt.Fprintf(out, "add them under applications.%s.commands, or run a raw command with %s %s -- <command>...\n",
				appKey, commandPath, appKey)
			return
		}
		fmt.Fprintf(out, "%s\n", style.Dim("no commands declared in "+manifest))
		fmt.Fprintf(out, "add them under applications.<app>.commands, or run a raw command with %s [app] -- <command>...\n", commandPath)
		return
	}
	fmt.Fprintf(out, "%s  %s\n\n", style.Dim("commands"), manifest)
	owners := map[string]int{}
	for _, key := range keys {
		commands := applications[key].Commands
		names := utils.SortedKeys(commands)
		width := 0
		for _, name := range names {
			width = max(width, len(name))
			owners[name]++
		}
		fmt.Fprintln(out, style.Bold(key))
		for _, name := range names {
			fmt.Fprintf(out, "  %-*s  %s\n", width, name, style.Dim(shellWords(commands[name])))
		}
		fmt.Fprintln(out)
	}
	hint := fmt.Sprintf("run one with %s <name>", commandPath)
	if appKey != "" {
		hint = fmt.Sprintf("run one with %s %s <name>", commandPath, appKey)
	} else if slices.ContainsFunc(keys, func(key string) bool {
		for name := range applications[key].Commands {
			if owners[name] > 1 {
				return true
			}
		}
		return false
	}) {
		hint += fmt.Sprintf(", or %s <app> <name> for a name several applications declare", commandPath)
	}
	fmt.Fprintln(out, style.Dim(hint))
}

// shellWords joins argv the way a shell would read it back, quoting words
// that carry whitespace or shell metacharacters.
func shellWords(argv []string) string {
	words := make([]string, len(argv))
	for i, word := range argv {
		if word == "" || strings.ContainsAny(word, " \t\n'\"\\$`&|;<>()*?[]{}#~!") {
			word = "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
		}
		words[i] = word
	}
	return strings.Join(words, " ")
}
