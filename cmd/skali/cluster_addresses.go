package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

// newClusterAddressesCmd declares this host's addresses after the fact. It
// exists because the declaration is what the coordinator binds and what
// other nodes are told to join through, and a cluster installed before the
// declaration existed has both pinned to whatever k3s picked.
func newClusterAddressesCmd() *cobra.Command {
	var clusterIP string
	var publicIPs, extraSANs, coordinatorBind []string
	var assumeYes bool
	cmd := &cobra.Command{
		Use:   "addresses",
		Short: "Declare how this host is addressed inside and outside the cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := os.Stdout
			reader := bufio.NewReader(os.Stdin)

			if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
				return err
			}
			detected, err := installer.Detect(ctx, runner())
			if err != nil {
				return err
			}
			record := detected.Record
			if record == nil {
				return fmt.Errorf("no readable skali installation record on this host (state %s)",
					detected.State)
			}

			declared := installer.NodeNetwork{
				ClusterIP: clusterIP, PublicIPs: publicIPs, ExtraSANs: extraSANs,
				CoordinatorBind: coordinatorBind,
			}
			if declared.IsZero() {
				if !cliprompt.Interactive() {
					return errors.New("non-interactive runs must declare at least one address")
				}
				declared, err = promptNodeNetwork(ctx, out, reader)
				if err != nil {
					return err
				}
				if declared.IsZero() {
					return errors.New("no addresses were declared")
				}
			} else if declared.ClusterIP == "" {
				// Flag runs that only add public names keep the recorded
				// cluster address rather than silently re-resolving it.
				declared.ClusterIP = record.Node.IP
			}
			resolved, err := installer.ResolveNodeNetwork(ctx, runner(), declared)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "  current  %s\n", describeNetwork(record.Node.Network()))
			fmt.Fprintf(out, "  declared %s\n", describeNetwork(resolved))
			if !assumeYes {
				confirmed, err := promptSession(out, reader).Confirm(ctx, cliprompt.ConfirmOptions{
					Title:   "Record this declaration for " + record.Node.Name + "?",
					Default: false,
				})
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Fprintln(out, "No changes were made.")
					return nil
				}
			}

			record.Node.SetNetwork(resolved)
			if err := installer.SaveRecord(ctx, runner(), record); err != nil {
				return err
			}
			warnings := publishNodeAddresses(ctx, out, record, resolved)
			warnings = append(warnings, liveAddressWarning(ctx, record, resolved)...)
			printWarnings(out, warnings)
			fmt.Fprintln(out, "Run `skali cluster diagnose` and `skali cluster repair` on this node to "+
				"widen the api certificate and rebind the coordinator.")
			return nil
		},
	}
	cmd.Flags().StringVar(&clusterIP, "cluster-ip", "", "address other cluster nodes reach this node through")
	cmd.Flags().StringSliceVar(&publicIPs, "public-ip", nil, "address reachable from outside the cluster network; repeatable")
	cmd.Flags().StringSliceVar(&extraSANs, "tls-san", nil, "additional name or address for the kubernetes api certificate; repeatable")
	cmd.Flags().StringSliceVar(&coordinatorBind, "coordinator-bind", nil,
		"scopes the enrollment coordinator listens on: cluster, public, or both")
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "record the declaration without confirmation")
	return cmd
}

// publishNodeAddresses moves the cluster-wide view of this server: the
// endpoint other nodes enroll through and the k3s endpoint they join
// through both derive from it. Failures degrade to warnings, because the
// local declaration is already durable and the cluster may be down.
func publishNodeAddresses(ctx context.Context, out *os.File, record *installer.Record,
	network installer.NodeNetwork) []string {
	if !record.Reconciled() || record.Node.Role != layout.RoleServer || network.ClusterIP == "" {
		return nil
	}
	store, _, err := reconciledClusterStore(ctx)
	if err != nil {
		return []string{"the cluster state was not updated (" + err.Error() +
			"); rerun this command once the api answers"}
	}
	endpoint := "https://" + net.JoinHostPort(network.ClusterIP, clusterstate.DefaultCoordinatorPort)
	_, err = store.Update(ctx, func(state *clusterstate.State) error {
		node, ok := state.Nodes[record.Node.ID]
		if !ok {
			return fmt.Errorf("node %s is not enrolled in the cluster state", record.Node.Name)
		}
		node.NodeIP = network.ClusterIP
		node.Coordinator = endpoint
		node.UpdatedAt = time.Now().UTC()
		state.Nodes[record.Node.ID] = node
		return nil
	})
	if err != nil {
		return []string{"the cluster state was not updated (" + err.Error() + ")"}
	}
	fmt.Fprintf(out, "  endpoint %s\n", endpoint)

	if record.Coordinator != nil {
		record.Coordinator.Endpoints = []string{endpoint}
		if err := installer.SaveRecord(ctx, runner(), record); err != nil {
			return []string{"the local coordinator endpoint was not updated (" + err.Error() + ")"}
		}
	}
	config, err := installer.LoadAgentConfig(ctx, runner())
	if err != nil {
		return nil
	}
	if err := installer.UpdateAgentEndpoints(ctx, runner(), config, []string{endpoint}); err != nil {
		return []string{"the local agent endpoint was not updated (" + err.Error() + ")"}
	}
	return nil
}

// liveAddressWarning states the one thing this command cannot do. k3s keeps
// advertising the address it registered with, and an etcd member's
// advertised address moves only by reinstalling that node, so pod and
// control-plane traffic stays on the old interface until then.
func liveAddressWarning(ctx context.Context, record *installer.Record,
	network installer.NodeNetwork) []string {
	if network.ClusterIP == "" || record.Node.Name == "" ||
		record.Node.Role != layout.RoleServer {
		return nil
	}
	live := installer.ServerJoinHost(ctx, runner(), record.Node.Name)
	if live == "" || live == record.Node.Name || live == network.ClusterIP {
		return nil
	}
	return []string{fmt.Sprintf("k3s still advertises %s for node %s; the api and enrollment "+
		"answer on %s once the certificate is widened, but node-to-node traffic moves only "+
		"when this node is reinstalled", live, record.Node.Name,
		strings.TrimSpace(network.ClusterIP))}
}
