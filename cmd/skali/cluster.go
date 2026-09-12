package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/installer/limavm"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// newClusterCommand builds the privileged installation and recovery group.
// It owns host-level k3s
// lifecycle and the installer-owned Skali system bundle; it never depends
// on the Skali API or product database, and no other skali command can
// perform any action offered here. Interactive by default: running it bare
// detects the host state and offers the operations appropriate to that
// state, while --config files drive explicit non-interactive runs.
func newClusterCommand() *cobra.Command {
	cluster := &cobra.Command{
		Use:   "cluster",
		Short: "Install, maintain, and recover a Skali cluster",
		// The nearest PersistentPreRun wins in cobra, so this fires for
		// every cluster subcommand and never for the developer-workflow
		// commands; the root's own hook only records completion requests.
		PersistentPreRun: func(command *cobra.Command, args []string) {
			useManagedLima()
		},
		RunE: func(command *cobra.Command, args []string) error {
			return runClusterRoot(command)
		},
	}

	cluster.PersistentFlags().StringVar(&vmFlag, "vm", limavm.DefaultInstance,
		"name of the Lima VM hosting the skali node (macOS only)")
	cluster.PersistentFlags().StringVar(&imageTarFlag, "image-tar", "",
		"docker-save tar of the skalid image, imported into the node during init (source installs)")
	cluster.PersistentFlags().StringVar(&hostdBinFlag, "hostd-bin", "",
		"Linux skali-hostd binary to install on managed nodes (source installs)")
	cluster.AddCommand(newClusterCreateCommand(), newClusterInstallCommand(), newClusterInitCommand(), newClusterStatusCommand(),
		newClusterUpgradeCommand(), newClusterUninstallCommand(), newClusterTokenCommand(), newClusterJoinCommand(),
		newClusterTierCommand(), newClusterDiagnoseCommand(), newClusterRepairCommand(), newClusterRestoreCommand(),
		newClusterNodeCommand(), newClusterChangesCommand(), newClusterPlanCommand(), newClusterApplyCommand(),
		newClusterRebalanceCommand(), newClusterAddressesCommand(), newClusterStorageMigrateCommand(),
		newClusterResetPasswordCommand())
	clusterCmd = cluster
	return cluster
}

// activeRunner is the host boundary; Linux keeps Local, and on macOS the
// darwin prelude swaps in the Lima runner once the VM is known running.
var activeRunner host.Runner = host.Local{}

// runner is called by every cluster command; it mutates the target host and
// never the machine the operator types on.
func runner() host.Runner {
	return activeRunner
}

var (
	vmFlag       string
	imageTarFlag string
	hostdBinFlag string
	clusterCmd   *cobra.Command
)

func vmFlagChanged() bool {
	return clusterCmd != nil && clusterCmd.PersistentFlags().Changed("vm")
}

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
