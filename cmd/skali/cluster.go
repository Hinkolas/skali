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

// newClusterCommand builds the privileged installation and recovery group
// (transcripts: docs/transcripts/cli-cluster.md). It owns host-level k3s
// lifecycle and the installer-owned Skali system bundle; it never depends
// on the Skali API or product database, and no other skali command can
// perform any action offered here. Interactive by default: running it bare
// detects the host state and offers the operations appropriate to that
// state, while --config files drive explicit non-interactive runs.
func newClusterCommand() *cobra.Command {
	cluster := &cobra.Command{
		Use:   "cluster",
		Short: "Install, maintain, and recover a Skali cluster",
		// The nearest PersistentPreRun wins in cobra and the skali root has
		// none, so this fires for every cluster subcommand and never for
		// the developer-workflow commands. Existing-cluster mode owns only
		// the bundle in a cluster it does not administer, so it never
		// touches host lifecycle: no Lima, no darwin prelude, no root.
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if !existingClusterMode() {
				useManagedLima()
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterRoot(cmd)
		},
	}

	cluster.PersistentFlags().StringVar(&vmFlag, "vm", limavm.DefaultInstance,
		"name of the Lima VM hosting the skali node (macOS only)")
	cluster.PersistentFlags().StringVar(&imageTarFlag, "image-tar", "",
		"docker-save tar of the skalid image, imported into the node during init (source installs)")
	cluster.PersistentFlags().StringVar(&hostdBinFlag, "hostd-bin", "",
		"Linux skali-hostd binary to install on managed nodes (source installs)")
	cluster.PersistentFlags().StringVar(&modeFlag, "mode", "",
		"installation mode: empty for managed k3s, or existing-cluster to install only the bundle")
	cluster.PersistentFlags().StringVar(&kubeconfigFlag, "kubeconfig", "",
		"explicit kubeconfig for existing-cluster mode (never the ambient one)")
	cluster.AddCommand(newClusterCreateCmd(), newClusterInstallCmd(), newClusterInitCmd(), newClusterStatusCmd(),
		newClusterUpgradeCmd(), newClusterUninstallCmd(), newClusterTokenCmd(), newClusterJoinCmd(),
		newClusterTierCmd(), newClusterDiagnoseCmd(), newClusterRepairCmd(), newClusterRestoreCmd(),
		newClusterNodeCmd(), newClusterChangesCmd(), newClusterPlanCmd(), newClusterApplyCmd(),
		newClusterRebalanceCmd(), newClusterAddressesCmd())
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
	vmFlag         string
	imageTarFlag   string
	hostdBinFlag   string
	modeFlag       string
	kubeconfigFlag string
	clusterCmd     *cobra.Command
)

func vmFlagChanged() bool {
	return clusterCmd != nil && clusterCmd.PersistentFlags().Changed("vm")
}

// existingClusterMode reports whether --mode existing-cluster is set.
func existingClusterMode() bool {
	return modeFlag == "existing-cluster"
}

// validateModeFlags enforces the mode/kubeconfig contract: existing-cluster
// requires an explicit kubeconfig and refuses the host-lifecycle flags;
// --kubeconfig without the mode is an error; any other --mode value is
// rejected. It returns the loaded kube client for existing-cluster mode.
func validateExistingMode() error {
	switch modeFlag {
	case "", "managed":
		if kubeconfigFlag != "" {
			return fmt.Errorf("--kubeconfig applies only to --mode existing-cluster")
		}
		return nil
	case "existing-cluster":
		if kubeconfigFlag == "" {
			return fmt.Errorf("--mode existing-cluster requires --kubeconfig")
		}
		if vmFlagChanged() {
			return fmt.Errorf("--vm applies only to managed installations, not --mode existing-cluster")
		}
		if imageTarFlag != "" {
			return fmt.Errorf("--image-tar applies only to managed installations, not --mode existing-cluster")
		}
		return nil
	default:
		return fmt.Errorf("unknown --mode %q; expected existing-cluster or empty", modeFlag)
	}
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
