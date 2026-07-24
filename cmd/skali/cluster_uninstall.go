package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

func newClusterUninstallCmd() *cobra.Command {
	var scope, confirmName string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Skali bundle or this node, with scoped confirmation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout
			if existingClusterMode() {
				return runExistingUninstall(ctx, out, bufio.NewReader(os.Stdin), scope, confirmName)
			}
			if scope == "" && !cliprompt.Interactive() {
				return fmt.Errorf("non-interactive run requires --scope bundle|node and --confirm <cluster>")
			}
			reader := bufio.NewReader(os.Stdin)
			return runUninstallFlow(ctx, out, reader, scope, confirmName)
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "removal scope: bundle (keep bare k3s) or node (remove k3s and all skali state)")
	cmd.Flags().StringVar(&confirmName, "confirm", "", "cluster name, confirming the removal non-interactively")
	return cmd
}

// runUninstallFlow guards, scopes, confirms, and executes a removal. The
// menu path enters with empty scope/confirm; the flag path skips prompts.
func runUninstallFlow(ctx context.Context, out *os.File, reader *bufio.Reader, scope, confirmName string) error {
	if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
		return err
	}
	detected, err := installer.Detect(ctx, runner())
	if err != nil {
		return err
	}
	switch detected.State {
	case installer.StateServer, installer.StateAgent, installer.StateDamaged,
		installer.StateInterrupted, installer.StateOrphaned, installer.StateEnrolled:
	case installer.StateUnmanaged:
		return unmanagedError()
	default:
		return fmt.Errorf("nothing to uninstall on this host (state %s)", detected.State)
	}
	record := detected.Record
	if record == nil {
		return fmt.Errorf("the installation record is unreadable; remove %s manually after inspecting it",
			installer.RecordPath)
	}

	if scope == "" && record.Versions.Bundle == "" {
		scope = "node"
	}
	if scope == "" {
		choice, err := promptSession(out, reader).Select(ctx, cliprompt.SelectOptions{
			Title: fmt.Sprintf("What should be removed from %s?", hostLabel(detected)),
			Description: fmt.Sprintf("This host belongs to cluster %q.",
				record.Cluster),
			Options: []cliprompt.Option{
				{
					Label:       "Skali bundle",
					Description: "remove Skali, workloads, and data; keep bare k3s",
					Value:       "bundle",
				},
				{
					Label:       "This node",
					Description: "remove k3s and every trace of Skali from this host",
					Value:       "node",
				},
			},
		})
		if err != nil {
			return err
		}
		scope = choice
	}

	switch scope {
	case "bundle":
		if record.Versions.Bundle == "" {
			return fmt.Errorf("the Skali bundle was never initialized; use --scope node to remove the interrupted host install")
		}
		return uninstallBundle(ctx, out, reader, record, confirmName)
	case "node":
		return uninstallNode(ctx, out, reader, record, confirmName)
	default:
		return fmt.Errorf("unknown --scope %q; expected bundle or node", scope)
	}
}

func uninstallBundle(ctx context.Context, out *os.File, reader *bufio.Reader,
	record *installer.Record, confirmName string) error {
	client, err := installer.KubeClient(ctx, runner())
	if err != nil {
		return fmt.Errorf("removing the bundle needs the kubernetes api: %w", err)
	}
	inventory, err := installer.GatherBundleInventory(ctx, client)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, installer.DescribeBundleRemoval(inventory))
	fmt.Fprintln(out)
	confirmed, err := confirmCluster(ctx, out, reader, record.Cluster, confirmName)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("confirmation did not match the cluster name %q; nothing was removed", record.Cluster)
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	var reconciledStore *clusterstate.Store
	if record.Reconciled() {
		reconciledStore = &clusterstate.Store{Client: client.Clientset}
		if _, err := reconciledStore.Update(ctx, func(state *clusterstate.State) error {
			if state.CurrentOperation != "" {
				return fmt.Errorf("cluster operation %s is active; let it finish before removing the bundle",
					state.CurrentOperation)
			}
			state.ReconciliationPaused = true
			return nil
		}); err != nil {
			progress.Abort()
			return err
		}
		// Every coordinator observes the durable pause before namespace
		// deletion starts. This is bounded to one reconciliation interval.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(4 * time.Second):
		}
	}
	if err := installer.UninstallBundle(ctx, runner(), client, record, progress); err != nil {
		progress.Abort()
		if reconciledStore != nil {
			return fmt.Errorf("%w; coordinator reconciliation remains paused; run skali cluster repair after diagnosing the removal",
				err)
		}
		return err
	}
	if reconciledStore != nil {
		if _, err := reconciledStore.Update(ctx, func(state *clusterstate.State) error {
			revision, err := state.EditCandidate(time.Now(),
				func(_ map[string]clusterstate.RevisionNode,
					platform *clusterstate.PlatformState) error {
					platform.Enabled = false
					platform.RegistryNode = ""
					return nil
				})
			if err != nil {
				return err
			}
			state.ConvergedRevision = revision.ID
			state.CandidateRevision = revision.ID
			state.Platform = revision.Platform
			state.ReconciliationPaused = false
			return nil
		}); err != nil {
			progress.Abort()
			return fmt.Errorf("bundle was removed but the disabled revision could not be committed: %w; "+
				"coordinator reconciliation remains paused", err)
		}
	}
	progress.Done("")
	fmt.Fprintln(out, "\nBare k3s keeps running; `skali cluster init` reinstalls Skali.")
	return nil
}

func uninstallNode(ctx context.Context, out *os.File, reader *bufio.Reader,
	record *installer.Record, confirmName string) error {
	plan, err := installer.PlanNodeRemoval(ctx, runner(), record)
	if err != nil {
		return err
	}
	if record.Reconciled() && !record.EnrolledOnly() && plan.Total > 1 {
		return fmt.Errorf("reconciled nodes are removed declaratively; on a server run "+
			"`skali cluster node remove %s`, then `skali cluster apply`", record.Node.Name)
	}
	leavingServer := record.Node.Role == layout.RoleServer && plan.Total > 1
	mayLeaveStaleMembership := record.RegistrationMayHaveStarted() &&
		record.Join != nil && !plan.ClusterReachable

	fmt.Fprintln(out)
	switch {
	case record.Node.Role == layout.RoleAgent:
		fmt.Fprintf(out, "Removing this agent node takes it out of cluster %q:\n", record.Cluster)
		fmt.Fprintln(out, "  - workloads placed on this node lose their local data")
	case leavingServer:
		fmt.Fprintf(out, "Removing this server takes it out of cluster %q:\n", record.Cluster)
		fmt.Fprintln(out, "  - workloads placed on this node lose their local data")
		printQuorumConsequence(out, plan.Servers-1)
	default:
		fmt.Fprintf(out, "Removing this node destroys the cluster %q completely:\n", record.Cluster)
		fmt.Fprintln(out, "  - every project namespace, database, bucket, and all registry contents")
	}
	if darwinInfo != nil {
		fmt.Fprintf(out, "  - the Lima VM %s and every trace of skali on this Mac\n", darwinInfo.Instance)
	} else {
		fmt.Fprintln(out, "  - k3s itself and "+installer.StateDir)
	}
	if mayLeaveStaleMembership {
		fmt.Fprintln(out, "  - the cluster API is unreachable; local cleanup may leave stale node or etcd membership")
	}
	fmt.Fprintln(out)
	confirmed, err := confirmCluster(ctx, out, reader, record.Cluster, confirmName)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("confirmation did not match the cluster name %q; nothing was removed", record.Cluster)
	}
	if record.InstallationID == "" {
		if err := installer.PersistOrphanRecord(ctx, runner(), record); err != nil {
			return fmt.Errorf("record recovered ownership before uninstall: %w", err)
		}
		record, err = installer.LoadRecord(ctx, runner())
		if err != nil {
			return err
		}
	}

	if darwinInfo != nil {
		return uninstallDarwinNode(ctx, out, record)
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if record.Reconciled() && record.Node.Role == layout.RoleServer &&
		plan.Total <= 1 && plan.ClusterReachable {
		client, err := installer.KubeClient(ctx, runner())
		if err != nil {
			progress.Abort()
			return err
		}
		if err := installer.QuiesceReconciledCluster(ctx, runner(), client); err != nil {
			progress.Abort()
			return err
		}
		if record.Versions.Bundle != "" {
			if err := installer.UninstallBundle(ctx, runner(), client, record, progress); err != nil {
				progress.Abort()
				return err
			}
		}
		if err := installer.RemoveCoordinatorNamespace(ctx, client, progress); err != nil {
			progress.Abort()
			return err
		}
	}
	if err := installer.UninstallNode(ctx, runner(), record, plan, progress); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out, "\nThis host is fresh again.")
	if record.EnrolledOnly() {
		fmt.Fprintf(out, "The coordinator may still list candidate node %s; from a server run "+
			"`skali cluster node remove %s` and `skali cluster apply`.\n",
			record.Node.Name, record.Node.Name)
	} else if mayLeaveStaleMembership && record.Node.Role == layout.RoleAgent {
		fmt.Fprintf(out, "The node object %s remains in the cluster; "+
			"delete it from a server with `k3s kubectl delete node %s`.\n",
			record.Node.Name, record.Node.Name)
	} else if mayLeaveStaleMembership {
		fmt.Fprintf(out, "The cluster could not confirm removal of server %s. From a surviving server, "+
			"run `k3s kubectl delete node %s` and verify the control-plane membership before reusing this node name.\n",
			record.Node.Name, record.Node.Name)
	}
	return nil
}

// printQuorumConsequence words what the server count after removal means
// for etcd quorum.
func printQuorumConsequence(out *os.File, remaining int) {
	switch {
	case remaining == 1:
		fmt.Fprintln(out, "  - the cluster keeps running on its single remaining server")
	case remaining%2 == 0:
		fmt.Fprintf(out, "  - the remaining %d servers tolerate %d failure(s); "+
			"etcd quorum prefers an odd count, so rejoin a server soon\n",
			remaining, remaining-(remaining/2+1))
	}
}

// confirmCluster accepts either the pre-supplied --confirm value or a
// type-the-name-back prompt.
func confirmCluster(ctx context.Context, out *os.File, reader *bufio.Reader, cluster, supplied string) (bool, error) {
	if supplied != "" {
		return supplied == cluster, nil
	}
	if !cliprompt.Interactive() {
		return false, nil
	}
	return promptSession(out, reader).ConfirmTyped(ctx,
		"Confirm cluster removal",
		fmt.Sprintf("Type %q exactly to continue.", cluster),
		cluster)
}
