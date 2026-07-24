package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

func newClusterTokenCmd() *cobra.Command {
	var role, server string
	var allowedCapabilities []string
	var ttl time.Duration
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

			if detected.Record.Reconciled() {
				if server != "" {
					return fmt.Errorf("--server is not stored in reconciled tokens; supply the coordinator to cluster join")
				}
				client, err := installer.KubeClient(ctx, runner())
				if err != nil {
					return err
				}
				store := &clusterstate.Store{Client: client.Clientset}
				invitation, token, err := store.CreateInvitation(ctx, role, allowedCapabilities, ttl)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "one-time %s invitation for cluster %q (expires %s):\n",
					invitation.Role, detected.Record.Cluster,
					invitation.ExpiresAt.Format(time.RFC3339))
				fmt.Fprintf(out, "  sudo skali cluster join <coordinator> --token-file <file> --capabilities <list>\n")
				fmt.Fprintln(out)
				fmt.Fprintln(out, "enrollment token (write it to <file>, mode 0600):")
				fmt.Fprintf(out, "  %s\n", token)
				fmt.Fprintln(out)
				fmt.Fprintf(out, "invitation id: %s\n", invitation.ID)
				fmt.Fprintln(out, "The token carries authentication only; it contains no endpoint, role,")
				fmt.Fprintln(out, "cluster name, k3s token, or registry credential.")
				return nil
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
	cmd.Flags().StringVar(&server, "server", "", "legacy-only advertised HTTPS k3s endpoint (v2 tokens never contain an address)")
	cmd.Flags().StringSliceVar(&allowedCapabilities, "capabilities", nil,
		"optional capabilities this invitation allows (default: all)")
	cmd.Flags().DurationVar(&ttl, "ttl", 24*time.Hour, "invitation lifetime for reconciled clusters")
	cmd.AddCommand(newClusterTokenListCmd(), newClusterTokenRevokeCmd())
	return cmd
}

func newClusterTokenListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List reconciled-cluster invitations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			invitations, err := store.Invitations(cmd.Context())
			if err != nil {
				return err
			}
			if len(invitations) == 0 {
				fmt.Fprintln(os.Stdout, "no invitations")
				return nil
			}
			for _, invitation := range invitations {
				state := "active"
				if invitation.Revoked {
					state = "revoked"
				} else if invitation.UsedBy != "" {
					state = "used"
				} else if time.Now().After(invitation.ExpiresAt) {
					state = "expired"
				}
				fmt.Fprintf(os.Stdout, "%s  %-6s  %-8s  expires %s\n",
					invitation.ID, invitation.Role, state,
					invitation.ExpiresAt.Format(time.RFC3339))
			}
			return nil
		},
	}
}

func newClusterTokenRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke ID",
		Short: "Revoke an unused reconciled-cluster invitation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			if err := store.RevokeInvitation(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "revoked invitation %s\n", args[0])
			return nil
		},
	}
}
