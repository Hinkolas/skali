package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

func newJoinCmd() *cobra.Command {
	var server, tokenFile, role, cluster, nodeIP string
	var capabilities []string
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join this host to an existing cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if role != layout.RoleAgent {
				return fmt.Errorf("joining as an additional server is not implemented in this slice; " +
					"it arrives with a later milestone")
			}
			// The full flag set is the consent, like --config elsewhere;
			// join never prompts. Interactive enrollment lives in the
			// fresh-host flow of a bare run.
			if server == "" || tokenFile == "" || len(capabilities) == 0 {
				return fmt.Errorf("join requires --server, --token-file, and --capabilities")
			}

			tasks := clirender.NewTasks(out)
			progress := newTaskProgress(tasks)
			_, err := installer.Install(ctx, runner(), installer.InstallOptions{
				Cluster:      cluster,
				Role:         layout.RoleAgent,
				Capabilities: capabilities,
				Join:         &installer.JoinOptions{Server: server, TokenFile: tokenFile},
				NodeIP:       nodeIP,
				Progress:     progress,
			})
			if err != nil {
				progress.Abort()
				return err
			}
			progress.Done("")
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "URL of an existing k3s server, for example https://cp-1.internal:6443")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "path to a file holding the join token")
	cmd.Flags().StringVar(&role, "role", layout.RoleAgent, "k3s role of this host; only agent joins are available in this slice")
	cmd.Flags().StringSliceVar(&capabilities, "capabilities", nil, "designated workload capabilities for this node")
	cmd.Flags().StringVar(&cluster, "cluster", installer.DefaultCluster, "name of the cluster being joined")
	cmd.Flags().StringVar(&nodeIP, "node-ip", "", "IP address this node advertises inside the cluster (multi-homed hosts)")
	return cmd
}
