// Command skali is the workflow-oriented client of the Skali platform. It
// owns local manifest, build, terminal, and development-runtime workflows while
// remote state changes continue to go through the public skalid API. It also
// carries the privileged installation and recovery role as the `skali cluster`
// command group, whose engine never depends on the Skali API or product
// database; see cluster.go.
//
// Configuration lives in ~/.config/skali/config.yaml as kubectl-style named
// contexts (master URL + session token); see `skali context --help`.
package main

import (
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

	root.AddCommand(newAuthCmd(), newContextCmd(), newValidateCmd(), newCompileCmd(),
		newPlanCommand(), newDeployCommand(), newDevCommand(),
		newRunsCommand(), newRunCommand(), newLogsCommand(), newClusterCommand())

	if err := root.Execute(); err != nil {
		style := clirender.StyleFor(os.Stderr)
		fmt.Fprintln(os.Stderr, style.BoldRed("error:"), err)
		os.Exit(1)
	}
}

// userAgent identifies this device in session lists ("skali/<version> (host)").
func userAgent() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("skali/%s (%s)", versionpkg.Version, host)
}

// currentClient builds a client for the active context; token may be empty.
func currentClient() (*cliconfig.Config, string, *client.Client, error) {
	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, "", nil, err
	}
	name, ctx, err := cfg.Current()
	if err != nil {
		return nil, "", nil, err
	}
	return cfg, name, client.New(ctx.Master, ctx.Token, userAgent()), nil
}
