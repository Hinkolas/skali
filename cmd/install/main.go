// Command skali-installer is the privileged installation and recovery tool
// (built as bin/skali-installer). It owns host-level k3s lifecycle and the
// installer-owned Skali system bundle; it never depends on the Skali API
// or product database, and the developer CLI can never perform any action
// offered here. Interactive by default: running it detects the host state
// and offers the operations appropriate to that state, while --config
// files drive explicit non-interactive runs.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "skali-installer",
		Short:         "Install, maintain, and recover a Skali installation",
		Version:       versionpkg.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoot(cmd)
		},
	}

	root.AddCommand(newInstallCmd(), newInitCmd(), newStatusCmd(), newUninstallCmd())
	root.AddCommand(stubCommands()...)

	if err := root.Execute(); err != nil {
		style := clirender.StyleFor(os.Stderr)
		fmt.Fprintln(os.Stderr, style.BoldRed("error:"), err)
		os.Exit(1)
	}
}

// runner is the host boundary; every command mutates the host through it.
func runner() host.Runner {
	return host.Local{}
}

// banner prints the transcript-style version header.
func banner(out *os.File) {
	fmt.Fprintf(out, "skali-installer %s (k3s %s pinned)\n\n", versionpkg.Version, installer.K3sVersion)
}
