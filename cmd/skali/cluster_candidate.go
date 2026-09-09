package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/layout"
)

func newClusterNodeCmd() *cobra.Command {
	node := &cobra.Command{
		Use:   "node",
		Short: "Stage reconciled-cluster node changes",
	}
	node.AddCommand(newClusterNodeCapabilitiesCmd(), newClusterNodeRemoveCmd(),
		newClusterNodeRestoreCmd(), newClusterNodeForgetCmd())
	return node
}

func newClusterNodeCapabilitiesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "capabilities NODE CAPABILITY...",
		Short: "Stage a node capability set",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			capabilities := append([]string(nil), args[1:]...)
			slices.Sort(capabilities)
			capabilities = slices.Compact(capabilities)
			for _, capability := range capabilities {
				if !slices.Contains(layout.Capabilities, capability) {
					return fmt.Errorf("unknown capability %q; expected one of %s",
						capability, strings.Join(layout.Capabilities, ", "))
				}
			}
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			state, err := store.Update(cmd.Context(), func(state *clusterstate.State) error {
				_, err := state.EditCandidate(time.Now(),
					func(nodes map[string]clusterstate.RevisionNode, _ *clusterstate.PlatformState) error {
						node, ok := clusterstate.FindNodeByName(nodes, args[0])
						if !ok {
							return fmt.Errorf("node %q is not in the candidate", args[0])
						}
						node.Capabilities = capabilities
						nodes[node.ID] = node
						return nil
					})
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "staged capabilities for %s in candidate revision %s\n",
				args[0], state.CandidateRevision)
			printApplyGuidance()
			return nil
		},
	}
}

func newClusterNodeRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NODE",
		Short: "Stage a managed node for drain and removal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			enrolledOnly := false
			state, err := store.Update(cmd.Context(), func(state *clusterstate.State) error {
				var err error
				enrolledOnly, err = clusterstate.StageNodeRemoval(state, args[0], time.Now())
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "staged removal of %s in candidate revision %s\n",
				args[0], state.CandidateRevision)
			if enrolledOnly {
				fmt.Fprintln(os.Stdout,
					"enrollment cancelled; removed from the plan. Any connected agent will clean up its local enrollment state")
				return nil
			}
			printApplyGuidance()
			return nil
		},
	}
}

func newClusterNodeRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore NODE",
		Short: "Cancel a staged node removal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			state, err := store.Update(cmd.Context(), func(state *clusterstate.State) error {
				baseID := state.ConvergedRevision
				if state.TargetRevision != "" {
					baseID = state.TargetRevision
				}
				base := state.Revisions[baseID]
				node, ok := clusterstate.FindNodeByName(base.Nodes, args[0])
				if !ok {
					return fmt.Errorf("node %q is not present in the active target", args[0])
				}
				_, err := state.EditCandidate(time.Now(),
					func(nodes map[string]clusterstate.RevisionNode, _ *clusterstate.PlatformState) error {
						nodes[node.ID] = node
						return nil
					})
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "restored %s in candidate revision %s\n",
				args[0], state.CandidateRevision)
			return nil
		},
	}
}

func newClusterNodeForgetCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "forget NODE",
		Short: "Finalize an unreachable removed host without local cleanup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return errors.New("forgetting an unreachable host requires --force")
			}
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			_, err = store.Update(cmd.Context(), func(state *clusterstate.State) error {
				var nodeID string
				for _, node := range clusterstate.SortedNodes(state.Nodes) {
					id := node.ID
					if node.Name == args[0] && node.Phase != clusterstate.NodePhaseRemoved && node.Phase != clusterstate.NodePhaseCancelled {
						nodeID = id
						if node.Phase != clusterstate.NodePhaseFailed &&
							node.Phase != clusterstate.NodePhaseDraining &&
							node.Phase != clusterstate.NodePhaseAwaitingCleanup &&
							node.Phase != clusterstate.NodePhaseUninstalling {
							return fmt.Errorf("node %s is not awaiting removal cleanup", args[0])
						}
						node.Phase = clusterstate.NodePhaseRemoved
						node.LastError = "local cleanup was not confirmed; stale k3s may remain on the host"
						node.UpdatedAt = time.Now().UTC().Truncate(time.Second)
						state.Nodes[id] = node
						break
					}
				}
				if nodeID == "" {
					return fmt.Errorf("unknown node %q", args[0])
				}
				if state.CurrentOperation != "" {
					operation := state.Operations[state.CurrentOperation]
					if step, ok := operation.NodeSteps[nodeID]; ok &&
						step.Action == clusterstate.NodeActionRemove {
						step.Phase = clusterstate.StepComplete
						step.LastError = "forced forget; local cleanup unconfirmed"
						step.UpdatedAt = time.Now().UTC().Truncate(time.Second)
						operation.NodeSteps[nodeID] = step
						state.Operations[operation.ID] = operation
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "forgot %s; local k3s and Skali files may still exist on that host\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "accept possible stale k3s and agent state on the unreachable host")
	return cmd
}

func newClusterChangesCmd() *cobra.Command {
	changes := &cobra.Command{Use: "changes", Short: "Manage the candidate cluster revision"}
	changes.AddCommand(newClusterChangesImportCmd(), &cobra.Command{
		Use:   "discard",
		Short: "Discard every unapplied candidate change",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			state, err := store.Update(cmd.Context(), func(state *clusterstate.State) error {
				if state.TargetRevision != "" {
					state.CandidateRevision = state.TargetRevision
				} else {
					state.CandidateRevision = state.ConvergedRevision
				}
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "candidate reset to revision %s\n", state.CandidateRevision)
			return nil
		},
	})
	return changes
}

func newClusterChangesImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import CLUSTER-LAYOUT",
		Short: "Replace the candidate using already-enrolled nodes from a layout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			document, err := layout.ParseFile(args[0])
			if err != nil {
				return err
			}
			if diagnostics := layout.Validate(document); len(diagnostics) > 0 {
				return diagnostics
			}
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			state, err := store.Update(cmd.Context(), func(state *clusterstate.State) error {
				_, err := state.ReplaceCandidateLayout(document.Layout, time.Now())
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "candidate replaced from %s at revision %s\n",
				args[0], state.CandidateRevision)
			printApplyGuidance()
			return nil
		},
	}
}

func newClusterPlanCmd() *cobra.Command {
	var rebalance bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview the candidate cluster transition",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			state, err := store.Load(cmd.Context())
			if err != nil {
				return err
			}
			plan, err := candidatePlan(state, rebalance)
			if err != nil {
				return err
			}
			printClusterPlan(plan)
			return nil
		},
	}
	cmd.Flags().BoolVar(&rebalance, "rebalance-workloads", false,
		"include controlled rolling redistribution of healthy managed workloads")
	return cmd
}

func newClusterApplyCmd() *cobra.Command {
	var yes, rebalance, wait bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply every pending cluster topology change",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, _, err := reconciledClusterStore(cmd.Context())
			if err != nil {
				return err
			}
			state, err := store.Load(cmd.Context())
			if err != nil {
				return err
			}
			plan, err := candidatePlan(state, rebalance)
			if err != nil {
				return err
			}
			printClusterPlan(plan)
			if plan.Empty() {
				fmt.Fprintln(os.Stdout, "nothing to apply")
				return nil
			}
			if !yes {
				if !cliprompt.Interactive() {
					return errors.New("non-interactive apply requires --yes")
				}
				reader := bufio.NewReader(os.Stdin)
				title := "Apply this cluster revision?"
				if plan.Destructive {
					title = "Apply these destructive cluster changes?"
				}
				confirmed, err := promptSession(os.Stdout, reader).Confirm(
					cmd.Context(), cliprompt.ConfirmOptions{Title: title})
				if err != nil {
					return err
				}
				if !confirmed {
					return errors.New("cluster apply cancelled; nothing was changed")
				}
			}
			candidateID := state.CandidateRevision
			var operation clusterstate.Operation
			_, err = store.Update(cmd.Context(), func(current *clusterstate.State) error {
				if current.CandidateRevision != candidateID {
					return errors.New("candidate changed after planning; run cluster plan again")
				}
				_, created, err := clusterstate.FreezeCandidate(current, rebalance, time.Now())
				operation = created
				return err
			})
			if err != nil {
				return err
			}
			printAcceptedOperation(operation)
			if wait {
				return waitClusterOperation(cmd.Context(), store, operation.ID)
			}
			fmt.Fprintln(os.Stdout, "Reconciliation continues in the background. Run `skali cluster status`.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "apply without an interactive confirmation")
	cmd.Flags().BoolVar(&rebalance, "rebalance-workloads", false,
		"also redistribute eligible healthy managed workloads")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the target revision converges")
	return cmd
}

func newClusterRebalanceCmd() *cobra.Command {
	var workloads, yes, wait bool
	cmd := &cobra.Command{
		Use:   "rebalance",
		Short: "Reconcile the current topology and optionally redistribute workloads",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !workloads {
				return errors.New("the current topology is continuously reconciled; use --workloads to redistribute healthy workloads")
			}
			apply := newClusterApplyCmd()
			args := []string{"--rebalance-workloads"}
			if yes {
				args = append(args, "--yes")
			}
			if wait {
				args = append(args, "--wait")
			}
			apply.SetArgs(args)
			apply.SetContext(cmd.Context())
			return apply.Execute()
		},
	}
	cmd.Flags().BoolVar(&workloads, "workloads", false, "roll eligible managed workloads to use the current topology")
	cmd.Flags().BoolVar(&yes, "yes", false, "rebalance without an interactive confirmation")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for rebalancing to complete")
	return cmd
}

func candidatePlan(state *clusterstate.State, rebalance bool) (clusterstate.Plan, error) {
	if state.CurrentOperation != "" {
		if operation, ok := state.Operations[state.CurrentOperation]; ok &&
			operation.Phase != clusterstate.OperationComplete {
			from, fromOK := state.Revisions[operation.FromRevision]
			target, targetOK := state.Revisions[operation.TargetRevision]
			if !fromOK || !targetOK {
				return clusterstate.Plan{}, errors.New(
					"the active cluster operation references a missing revision")
			}
			return clusterstate.BuildPlan(state, from, target,
				operation.RebalanceWorkloads)
		}
	}
	from, err := state.Converged()
	if err != nil {
		return clusterstate.Plan{}, err
	}
	target, err := state.Candidate()
	if err != nil {
		return clusterstate.Plan{}, err
	}
	return clusterstate.BuildPlan(state, from, target, rebalance)
}

func printClusterPlan(plan clusterstate.Plan) {
	renderClusterPlan(os.Stdout, plan)
}

func renderClusterPlan(out io.Writer, plan clusterstate.Plan) {
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "%s %s\n", style.BrightCyan("◆"), style.Bold("Cluster plan"))
	clusterPlanRow(out, style, "from", shortRevision(plan.FromRevision))
	clusterPlanRow(out, style, "target", shortRevision(plan.TargetRevision))
	if plan.Empty() {
		clusterPlanRow(out, style, "actions", style.Muted("none"))
		return
	}
	fmt.Fprintln(out, "  "+style.Muted("actions"))
	for _, action := range plan.Actions {
		title := strings.ReplaceAll(string(action.Kind), "-", " ")
		if action.NodeName != "" {
			title += " · " + action.NodeName
		}
		fmt.Fprintf(out, "    %s %s\n", style.Glyph("pending", 0), title)
		if len(action.From) > 0 || len(action.To) > 0 {
			from, to := strings.Join(action.From, ", "), strings.Join(action.To, ", ")
			if from == "" {
				from = "none"
			}
			if to == "" {
				to = "none"
			}
			fmt.Fprintf(out, "      %s\n", style.Muted(from+" → "+to))
		}
		if action.Detail != "" {
			fmt.Fprintf(out, "      %s\n", style.Muted(action.Detail))
		}
	}
	if plan.DatabaseTierFrom != plan.DatabaseTierTo {
		clusterPlanRow(out, style, "database",
			fmt.Sprintf("%s → %s", plan.DatabaseTierFrom, plan.DatabaseTierTo))
	}
	for _, warning := range plan.Warnings {
		clusterPlanRow(out, style, "warning", style.BrightYellow(warning))
	}
}

func clusterPlanRow(out io.Writer, style *clirender.Style, label, value string) {
	fmt.Fprintf(out, "  %s %s\n", style.Muted(fmt.Sprintf("%-10s", label)), value)
}

func printAcceptedOperation(operation clusterstate.Operation) {
	style := clirender.StyleFor(os.Stdout)
	printStatusRow(os.Stdout, style, "operation",
		shortRevision(operation.ID)+" "+style.BrightGreen("accepted"))
}

func waitClusterOperation(ctx context.Context, store *clusterstate.Store, operationID string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		state, err := store.Load(ctx)
		if err != nil {
			return err
		}
		operation, ok := state.Operations[operationID]
		if !ok {
			return errors.New("cluster operation disappeared")
		}
		switch operation.Phase {
		case clusterstate.OperationComplete:
			fmt.Fprintf(os.Stdout, "cluster converged at revision %s\n", operation.TargetRevision)
			return nil
		case clusterstate.OperationFailed:
			return fmt.Errorf("cluster operation failed: %s", operation.LastError)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func printApplyGuidance() {
	fmt.Fprintln(os.Stdout, "No cluster services were modified.")
	fmt.Fprintln(os.Stdout, "Run `skali cluster plan`, then `skali cluster apply`.")
}
