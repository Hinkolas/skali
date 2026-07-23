package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

func newClusterTokenCmd() *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the join command for this cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if existingClusterMode() {
				return existingModeRefusal("join tokens")
			}
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

			token, err := installer.CreateJoinToken(ctx, runner(), detected.Record, role)
			if err != nil {
				return err
			}
			if token.Role == layout.RoleServer {
				fmt.Fprintf(out, "join command for cluster %q (server token, never expires):\n",
					token.Cluster)
			} else {
				fmt.Fprintf(out, "join command for cluster %q (token expires in %s):\n",
					token.Cluster, token.Expires)
			}
			fmt.Fprintf(out, "  sudo skali cluster join --server %s \\\n", token.ServerURL)
			fmt.Fprintf(out, "    --token-file <file> --role %s --capabilities <list>", token.Role)
			if token.Cluster != installer.DefaultCluster {
				fmt.Fprintf(out, " --cluster %s", token.Cluster)
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out)
			fmt.Fprintln(out, "join token (write it to <file> on the joining host, mode 0600):")
			fmt.Fprintf(out, "  %s\n", token.Token)
			if token.Role == layout.RoleServer {
				fmt.Fprintln(out)
				fmt.Fprintln(out, "warning: this is the cluster's permanent server token; it grants full")
				fmt.Fprintln(out, "administrator access and never expires. Delete the token file on the")
				fmt.Fprintln(out, "joining host once the join completes.")
				if servers, err := installer.CountServers(ctx, runner()); err == nil && servers%2 == 1 {
					fmt.Fprintln(out)
					fmt.Fprintf(out, "note: this join would make %d servers; etcd quorum prefers one or three\n",
						servers+1)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", layout.RoleAgent, "role the token enrolls: agent or server")
	return cmd
}
