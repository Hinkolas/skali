package main

import (
	"bufio"
	"context"
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

func newUninstallCmd() *cobra.Command {
	var scope, confirmName string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Skali bundle or this node, with scoped confirmation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout
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
	detected, err := installer.Detect(ctx, runner())
	if err != nil {
		return err
	}
	switch detected.State {
	case installer.StateServer, installer.StateAgent, installer.StateDamaged:
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

	if scope == "" {
		fmt.Fprintf(out, "scope of removal on host %s (cluster %q):\n\n", hostLabel(detected), record.Cluster)
		choice, err := cliprompt.Select(reader, out, "  : ", []string{
			"skali bundle    remove Skali and all project workloads and data; keep bare k3s running",
			"this node       remove k3s and every trace of skali from this host",
		}, -1)
		if err != nil {
			return err
		}
		scope = []string{"bundle", "node"}[choice]
	}

	switch scope {
	case "bundle":
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
	if !confirmCluster(reader, record.Cluster, confirmName) {
		return fmt.Errorf("confirmation did not match the cluster name %q; nothing was removed", record.Cluster)
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if err := installer.UninstallBundle(ctx, runner(), client, record, progress); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out, "\nBare k3s keeps running; `skali-installer init` reinstalls Skali.")
	return nil
}

func uninstallNode(ctx context.Context, out *os.File, reader *bufio.Reader,
	record *installer.Record, confirmName string) error {
	// Removing the only node destroys the cluster; count members when the
	// API answers, and fall back to the single-node assumption this slice
	// can actually create when it does not.
	nodeCount := 1
	if client, err := installer.KubeClient(ctx, runner()); err == nil {
		if nodes, err := client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
			nodeCount = len(nodes.Items)
		}
	}
	if nodeCount > 1 {
		return fmt.Errorf("removing a node from a multi-node cluster is not implemented in this slice")
	}

	fmt.Fprintln(out)
	if record.Node.Role == layout.RoleAgent {
		fmt.Fprintf(out, "Removing this agent node takes it out of cluster %q:\n", record.Cluster)
		fmt.Fprintln(out, "  - workloads placed on this node lose their local data")
		fmt.Fprintln(out, "  - k3s itself and "+installer.StateDir)
	} else {
		fmt.Fprintf(out, "Removing this node destroys the cluster %q completely:\n", record.Cluster)
		fmt.Fprintln(out, "  - every project namespace, database, bucket, and all registry contents")
		fmt.Fprintln(out, "  - k3s itself and "+installer.StateDir)
	}
	fmt.Fprintln(out)
	if !confirmCluster(reader, record.Cluster, confirmName) {
		return fmt.Errorf("confirmation did not match the cluster name %q; nothing was removed", record.Cluster)
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	if err := installer.UninstallNode(ctx, runner(), record, nodeCount, progress); err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out, "\nThis host is fresh again.")
	if record.Node.Role == layout.RoleAgent {
		fmt.Fprintf(out, "The node object %s remains in the cluster; "+
			"delete it from a server with `k3s kubectl delete node %s`.\n",
			record.Node.Name, record.Node.Name)
	}
	return nil
}

// confirmCluster accepts either the pre-supplied --confirm value or a
// type-the-name-back prompt.
func confirmCluster(reader *bufio.Reader, cluster, supplied string) bool {
	if supplied != "" {
		return supplied == cluster
	}
	if !cliprompt.Interactive() {
		return false
	}
	return cliprompt.ConfirmTyped(reader,
		fmt.Sprintf("Type the cluster name (%s) to continue: ", cluster), cluster)
}
