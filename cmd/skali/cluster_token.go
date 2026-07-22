package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/installer"
)

func newClusterTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the join command for this cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
				return err
			}
			detected, err := installer.Detect(ctx, runner())
			if err != nil {
				return err
			}
			switch detected.State {
			case installer.StateServer:
			case installer.StateAgent:
				return fmt.Errorf("join tokens are created on a server node; this host joined as an agent")
			case installer.StateUnmanaged:
				return unmanagedError()
			case installer.StateDamaged:
				return fmt.Errorf("this installation is damaged: %s",
					strings.Join(detected.Problems, "; "))
			default:
				return fmt.Errorf("no skali installation on this host (state %s); run install first",
					detected.State)
			}

			token, err := installer.CreateJoinToken(ctx, runner(), detected.Record)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "join command for cluster %q (token expires in %s):\n",
				token.Cluster, installer.JoinTokenTTL)
			fmt.Fprintf(out, "  sudo skali cluster join --server %s \\\n", token.ServerURL)
			fmt.Fprintf(out, "    --token-file <file> --role agent --capabilities <list>")
			if token.Cluster != installer.DefaultCluster {
				fmt.Fprintf(out, " --cluster %s", token.Cluster)
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out)
			fmt.Fprintln(out, "join token (write it to <file> on the joining host, mode 0600):")
			fmt.Fprintf(out, "  %s\n", token.Token)
			return nil
		},
	}
}
