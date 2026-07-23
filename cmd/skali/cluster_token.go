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
	var role, server string
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

			if server != "" {
				server, err = installer.NormalizeJoinServer(server)
				if err != nil {
					return err
				}
			}
			token, err := installer.CreateJoinTokenForServer(ctx, runner(), detected.Record, role, server)
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
			fmt.Fprintln(out, "  sudo skali cluster join --token-file <file> --capabilities <list>")
			fmt.Fprintf(out, "  endpoint encoded in token: %s\n", token.ServerURL)
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
	cmd.Flags().StringVar(&server, "server", "", "advertised HTTPS join endpoint (defaults to this node's InternalIP:6443)")
	return cmd
}
