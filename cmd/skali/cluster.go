package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// newClusterCommand builds the Linux host installation and recovery group.
// The normal upgrade command uses the remote API and works on every CLI
// platform. Other commands operate directly on the managed Linux host.
// Running the group bare opens its maintenance menu.
func newClusterCommand() *cobra.Command {
	cluster := &cobra.Command{
		Use:   "cluster",
		Short: "Install, maintain, and recover a Skali cluster",
		PersistentPreRunE: func(command *cobra.Command, args []string) error {
			recover, _ := command.Flags().GetBool("recover")
			if command.Name() == "upgrade" && !recover && imageTarFlag == "" {
				return nil
			}
			return requireClusterHost(runtime.GOOS)
		},
		RunE: func(command *cobra.Command, args []string) error {
			return runClusterRoot(command)
		},
	}

	cluster.PersistentFlags().StringVar(&imageTarFlag, "image-tar", "",
		"docker-save tar of the skalid image, imported into the node during init (source installs)")
	cluster.PersistentFlags().StringVar(&hostdBinFlag, "hostd-bin", "",
		"Linux skali-hostd binary to install on managed nodes (source installs)")
	cluster.AddCommand(newClusterCreateCommand(), newClusterInstallCommand(), newClusterInitCommand(), newClusterStatusCommand(),
		newClusterUpgradeCommand(), newClusterUninstallCommand(), newClusterTokenCommand(), newClusterJoinCommand(),
		newClusterTierCommand(), newClusterDiagnoseCommand(), newClusterRepairCommand(), newClusterRestoreCommand(),
		newClusterNodeCommand(), newClusterChangesCommand(), newClusterPlanCommand(), newClusterApplyCommand(),
		newClusterRebalanceCommand(), newClusterAddressesCommand(),
		newClusterResetPasswordCommand())
	return cluster
}

// activeRunner is the Linux host boundary; tests can substitute a scripted host.
var activeRunner host.Runner = host.Local{}

// runner is the boundary for operations on the Linux host running the command.
func runner() host.Runner {
	return activeRunner
}

var (
	imageTarFlag string
	hostdBinFlag string
)

// banner prints the transcript-style version header.
func banner(out *os.File) {
	style := clirender.StyleFor(out)
	fmt.Fprintln(out, style.Bold("Skali cluster"))
	fmt.Fprintf(out, "  %s %s\n",
		style.Muted(fmt.Sprintf("%-8s", "version")), versionpkg.Version)
	fmt.Fprintf(out, "  %s %s %s\n\n",
		style.Muted(fmt.Sprintf("%-8s", "k3s")),
		installer.K3sVersion,
		style.Muted("(pinned)"))
}

func requireClusterHost(goos string) error {
	if goos != "linux" {
		return fmt.Errorf("cluster hosts require Linux (Debian or Ubuntu); run this command on the server. The macOS CLI supports remote operations and Docker-based skali dev")
	}
	return nil
}

func readHostFile(ctx context.Context, path string) ([]byte, error) {
	return runner().ReadFile(ctx, path)
}

func printWarnings(out *os.File, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintln(out)
	for _, warning := range warnings {
		fmt.Fprintln(out, warning)
	}
}
