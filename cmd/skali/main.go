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
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "skali",
		Short:         "Deploy apps and manage skali clusters",
		Version:       versionpkg.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newRemoteCmd(), newValidateCmd(), newCompileCmd(),
		newPlanCommand(), newDeployCommand(), newRollbackCommand(),
		newDevCommand(), newRunCommand(), newLogsCommand(), newExecCommand(),
		newValuesCommand(), newBackupCommand(), newEnvCommand(), newAccessCommand(),
		newClusterCommand(), newSkillCommand(), newUpgradeCommand())

	if err := root.Execute(); err != nil {
		// A remote exec command's own exit status is a result, not an
		// error: pass it through silently, the process already wrote its
		// stderr through the session.
		if exit, ok := errors.AsType[*client.ExecExitError](err); ok {
			os.Exit(exit.Code)
		}
		style := clirender.StyleFor(os.Stderr)
		fmt.Fprintln(os.Stderr, style.BoldRed("error:"), err)
		if mismatch, ok := errors.AsType[*client.InstanceMismatchError](err); ok {
			fmt.Fprintln(os.Stderr, instanceMismatchHint(mismatch.Master))
		}
		os.Exit(1)
	}
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

// userAgent identifies this device in session lists ("skali/<version> (host)").
func userAgent() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("skali/%s (%s)", versionpkg.Version, host)
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
// observes is adopted into the config (trust on first use). The save is best
// effort; a failed adoption simply repeats on the next command.
func remoteClient(cfg *cliconfig.Config, remote *cliconfig.Remote) *client.Client {
	c := client.New(remote.Master, remote.Token, userAgent())
	c.PinInstance(remote.Instance, func(observed string) {
		remote.Instance = observed
		_ = cliconfig.Save(cfg)
	})
	return c
}
