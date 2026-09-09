package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
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

			// Flags stay authoritative: prompts fire only interactively
			// and only for what the operator did not already decide.
			if !cmd.Flags().Changed("role") && cliprompt.Interactive() {
				role, err = promptTokenRole(ctx, out, detected.Record.Reconciled())
				if err != nil {
					return err
				}
			}

			if detected.Record.Reconciled() {

				if !cmd.Flags().Changed("capabilities") && cliprompt.Interactive() {
					allowedCapabilities, err = promptInvitationCapabilities(ctx, out)
					if err != nil {
						return err
					}
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

				var endpoints []string
				if server != "" {
					endpoint, err := clusterstate.NormalizeEndpoint(server)
					if err != nil {
						return err
					}
					endpoints = append(endpoints, endpoint)
				} else if detected.Record.Coordinator != nil {
					endpoints = detected.Record.Coordinator.Endpoints
				}
				token, err = clusterstate.WithCoordinators(token, endpoints)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "  sudo skali cluster join --token '%s'\n", token)
				if len(endpoints) == 0 {
					fmt.Fprintln(out, "No coordinator endpoint is recorded; supply one when joining.")
				}
				if len(invitation.AllowedCapabilities) > 0 {
					fmt.Fprintf(out, "  capabilities: %s (used by default)\n", strings.Join(invitation.AllowedCapabilities, ", "))
				} else {
					fmt.Fprintln(out, "Choose this node's capabilities when joining, or pass --capabilities.")
				}
				fmt.Fprintf(out, "\ninvitation id: %s\n", invitation.ID)
				fmt.Fprintln(out, "The invitation includes coordinator discovery hints. Role and capabilities are verified by the coordinator.")

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
	cmd.Flags().StringVar(&role, "role", layout.RoleAgent, "role the token enrolls: agent or server (interactive runs ask)")
	cmd.Flags().StringVar(&server, "server", "", "advertised coordinator endpoint override (k3s endpoint for legacy clusters)")
	cmd.Flags().StringSliceVar(&allowedCapabilities, "capabilities", nil,
		"optional capabilities this invitation allows (default: all)")
	cmd.Flags().DurationVar(&ttl, "ttl", 24*time.Hour, "invitation lifetime for reconciled clusters")
	cmd.AddCommand(newClusterTokenListCmd(), newClusterTokenRevokeCmd())
	return cmd
}

// promptTokenRole asks which role the token should enroll and explains the
// asymmetry: agents add workload capacity with disposable credentials,
// servers join the control plane and its etcd quorum. What the choice
// hands out differs by record schema, so the wording does too: reconciled
// clusters mint one-time invitations for both roles, while a legacy server
// token is the permanent k3s credential itself.
func promptTokenRole(ctx context.Context, out *os.File, reconciled bool) (string, error) {
	description := "Agents run workloads; servers join the Kubernetes control plane and " +
		"its etcd quorum."
	server := "control plane and etcd member; can administer the cluster"
	if reconciled {
		description += " Both invitations are one-time and expiring: the k3s " +
			"credential travels only over the authenticated enrollment channel."
	} else {
		description += " An agent token expires after " + installer.JoinTokenTTL +
			". A server token is the cluster's permanent k3s credential: it never " +
			"expires and grants full administrator access, so delete the token " +
			"file once the join completes."
		server = "control plane and etcd member; permanent full-access credential"
	}
	return promptSession(out, bufio.NewReader(os.Stdin)).Select(ctx, cliprompt.SelectOptions{
		Title:       "Which role should the join enroll?",
		Description: description,
		Options: []cliprompt.Option{
			{Label: "Agent", Description: "runs workloads only; disposable expiring credential",
				Value: layout.RoleAgent},
			{Label: "Server", Description: server, Value: layout.RoleServer},
		},
		DefaultValue: layout.RoleAgent,
	})
}

// promptInvitationCapabilities asks what the joining node may run.
// Selecting the full set returns nil, the unrestricted default, so an
// invitation minted before a capability exists never restricts by
// accident.
func promptInvitationCapabilities(ctx context.Context, out *os.File) ([]string, error) {
	options := make([]cliprompt.Option, 0, len(layout.Capabilities))
	for _, capability := range layout.Capabilities {
		options = append(options, cliprompt.Option{Label: capability, Value: capability})
	}
	selected, err := promptSession(out, bufio.NewReader(os.Stdin)).MultiSelect(ctx,
		cliprompt.MultiSelectOptions{
			Title:         "What may the joining node run?",
			Description:   "Enrollment refuses capabilities outside this set.",
			Options:       options,
			DefaultValues: append([]string(nil), layout.Capabilities...),
			Validate: func(values []string) error {
				if len(values) == 0 {
					return errors.New("select at least one capability")
				}
				return nil
			},
		})
	if err != nil {
		return nil, err
	}
	if len(selected) == len(layout.Capabilities) {
		return nil, nil
	}
	return selected, nil
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
