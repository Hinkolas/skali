// Command skali is the workflow-oriented client of the Skali platform. It
// owns local manifest, build, terminal, and development-runtime workflows while
// remote state changes continue to go through the public skalid API. It also
// carries the privileged installation and recovery role as the `skali cluster`
// command group, whose engine never depends on the Skali API or product
// database; see cluster.go.
//
// Configuration lives in ~/.config/skali/config.yaml as named remotes
// (master URL + session token); see `skali remote --help`.
package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// verboseTranscript is the --verbose switch: live step tails show the
// server's full detail instead of the compact status rows.
var verboseTranscript bool

// newRootCommand assembles the whole CLI surface.
func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "skali",
		Short:         "Deploy apps and manage skali clusters",
		Version:       versionpkg.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// The completion group below replaces cobra's stock one so it can also
	// install the script.
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentPreRun = recordCompletionLine

	root.PersistentFlags().BoolVar(&verboseTranscript, "verbose", false,
		"show every recorded detail under live steps (TLS issuance fields, health snapshots) instead of the compact status rows")

	root.AddCommand(newRemoteCommand(), newValidateCommand(), newManifestCommand(), newCompileCommand(),
		newPlanCommand(), newDeployCommand(), newRollbackCommand(),
		newDevCommand(), newRunCommand(), newLogsCommand(), newExecCommand(),
		newValuesCommand(), newBackupCommand(), newEnvCommand(), newAccessCommand(),
		newClusterCommand(), newSkillCommand(), newUpgradeCommand(), newVersionCommand(), newCompletionCommand())
	registerCompletions(root)
	return root
}

// newVersionCommand prints the CLI version; the same text as --version,
// reachable as the verb people try first when filing a report. Below it,
// one line per remote names the skalid version last observed there and
// whether the matching skali is cached, so the dispatch state is never
// hidden (the first line stays exactly what scripts parse).
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			out := command.OutOrStdout()
			if _, err := fmt.Fprintf(out, "%s version %s\n", command.Root().Name(), versionpkg.Version); err != nil {
				return err
			}
			cfg, err := cliconfig.Load()
			if err != nil {
				return nil
			}
			for _, line := range remoteVersionLines(cfg, versionpkg.Version, installer.DefaultCacheDir()) {
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
}

// remoteVersionLines renders the per-remote dispatch state: recorded
// remotes only, the hidden local remote excluded, sorted by name.
func remoteVersionLines(cfg *cliconfig.Config, home, cacheDir string) []string {
	var lines []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Remotes)) {
		remote := cfg.Remotes[name]
		if name == localRemoteName || remote.Version == "" {
			continue
		}
		state := "not cached"
		switch {
		case remote.Version == home:
			state = "this binary"
		case !versionpkg.IsRelease(remote.Version):
			state = "development build, not dispatched"
		default:
			if _, err := os.Stat(installer.CLICachePath(cacheDir, remote.Version) + ".sha256"); err == nil {
				state = "cached"
			}
		}
		lines = append(lines, fmt.Sprintf("remote %s  skalid %s (%s)", name, remote.Version, state))
	}
	return lines
}

func main() {
	// Another release may own this command: see dispatch.go.
	if handled, code := dispatch(os.Args[1:]); handled {
		os.Exit(code)
	}
	err := newRootCommand().Execute()
	// A remote exec command's own exit status, or the status of a command
	// that handed itself to another release, is a result, not an error:
	// pass it through silently, the process already wrote its stderr.
	if code, ok := passthroughExit(err); ok {
		os.Exit(code)
	}
	// A cluster that moved since its record was written refuses home once;
	// the refusal recorded the new version, so dispatch can run the command
	// again with the right release (see dispatch.go).
	if handled, code := rerunAfterMismatch(err, os.Getenv, os.Stderr, dispatchTried, func() (bool, int) { return dispatch(os.Args[1:]) }); handled {
		os.Exit(code)
	}
	// A dispatched child refused as the wrong release says nothing: its
	// parent reruns the command with the right release, or prints the
	// failure itself.
	code := exitCodeFor(err)
	if code == exitVersionMoved {
		os.Exit(code)
	}
	style := clirender.StyleFor(os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, style.BoldRed("error:"), err)
		if mismatch, ok := errors.AsType[*client.InstanceMismatchError](err); ok {
			fmt.Fprintln(os.Stderr, instanceMismatchHint(mismatch.Master))
		}
	}
	// Version skew against the daemon is named once per invocation, on
	// success and on failure alike (the daemon's own refusal included), and
	// only on stderr: stdout stays clean for `$(skali remote token)`. Under
	// dispatch the skew is normally gone before this runs; it remains for
	// development builds, SKALI_NO_DISPATCH, and a fetch that failed.
	if hint := pendingSkewHint(); hint != "" {
		fmt.Fprintln(os.Stderr, style.Yellow(hint))
	}
	os.Exit(code)
}

// passthroughExit reports the exit status an error carries as a result
// rather than a failure of this process: a remote exec command's own status
// (its stderr already flowed through the session) or the status of the
// child a command handed itself to (dispatchedExit).
func passthroughExit(err error) (int, bool) {
	if exit, ok := errors.AsType[*client.ExecExitError](err); ok {
		return exit.Code, true
	}
	if exit, ok := errors.AsType[*dispatchedExit](err); ok {
		return exit.code, true
	}
	return 0, false
}

// instanceMismatchHint tells the user how to resolve a changed installation
// identity for the remote behind the given master URL.
func instanceMismatchHint(master string) string {
	if cfg, err := cliconfig.Load(); err == nil {
		if name, _, ok := lookupRemoteByMaster(cfg, master); ok {
			if name == localRemoteName {
				return "the local platform was recreated; run `skali dev` to log in to it again"
			}
			return fmt.Sprintf("to trust the new installation run `skali remote login %s`, or drop the remote with `skali remote remove %s`",
				name, name)
		}
	}
	return "to trust the new installation run `skali remote login`, or drop the remote with `skali remote remove`"
}

// caller identifies this CLI to the daemon: the session-list label
// ("skali/<version> (host)") and the build version the daemon's exact-match
// gate compares.
func caller() client.Caller {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return client.Caller{
		UserAgent: fmt.Sprintf("skali/%s (%s)", versionpkg.Version, host),
		Version:   versionpkg.Version,
	}
}

// currentClient builds a client for the current remote; token may be empty.
func currentClient() (*cliconfig.Config, string, *client.Client, error) {
	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, "", nil, err
	}
	name, remote, err := cfg.Current()
	if err != nil {
		return nil, "", nil, err
	}
	return cfg, name, remoteClient(cfg, remote), nil
}

// remoteClient builds the API client for a stored remote with install-identity
// verification armed: a changed identity fails requests with
// *client.InstanceMismatchError, and the first identity an unpinned remote
// observes is adopted into the config (trust on first use). The daemon
// version each response carries is recorded the same way: it feeds the skew
// hint for this invocation and, when it changed, the remote's version record
// that dispatch reads next time (the daemon's own refusal included, which is
// how a dispatched child leaves the moved version behind for its parent).
// Saves are best effort; a failed one simply repeats on the next command.
func remoteClient(cfg *cliconfig.Config, remote *cliconfig.Remote) *client.Client {
	c := client.New(remote.Master, remote.Token, caller())
	c.PinInstance(remote.Instance, func(observed string) {
		remote.Instance = observed
		_ = cliconfig.Save(cfg)
	})
	name, _, _ := lookupRemoteByMaster(cfg, remote.Master)
	c.OnVersion(func(observed string) {
		skew.record(name, observed)
		if remote.Version != observed {
			remote.Version = observed
			_ = cliconfig.Save(cfg)
		}
	})
	return c
}
