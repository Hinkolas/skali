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
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// runClusterRoot is the bare `skali cluster` entry: detect, render the state
// header, and offer the maintenance menu interactively. Detection is
// read-only; no maintenance action runs without being selected.
func runClusterRoot(cmd *cobra.Command) error {
	ctx := cmd.Context()
	out := os.Stdout

	if existingClusterMode() {
		return runExistingRoot(ctx, out)
	}
	banner(out)

	present, err := darwinPrelude(ctx, out, vmPolicyStatus, "")
	if err != nil {
		return err
	}
	if !present {
		printDarwinFreshHeader(out)
		if !cliprompt.Interactive() {
			fmt.Fprintln(out, "This host is not part of a Skali installation.")
			fmt.Fprintln(out, "Non-interactive installs run: skali cluster install --config node.yaml")
			return nil
		}
		return runInteractiveFreshFlow(ctx, out)
	}

	detected, err := installer.Detect(ctx, runner())
	if err != nil {
		return err
	}

	switch detected.State {
	case installer.StateUnsupported:
		return fmt.Errorf("this host cannot run a skali installation: %s",
			strings.Join(detected.Problems, "; "))
	case installer.StateUnmanaged:
		printFreshHeader(out, detected)
		return unmanagedError()
	case installer.StateInterrupted, installer.StateOrphaned:
		status, err := installer.GatherStatus(ctx, runner())
		if err != nil {
			return err
		}
		printStatus(out, status)
		if !cliprompt.Interactive() {
			return nil
		}
		return runRecoveryMenu(ctx, out, status)
	case installer.StateFresh:
		printFreshHeader(out, detected)
		if !cliprompt.Interactive() {
			fmt.Fprintln(out, "This host is not part of a Skali installation.")
			fmt.Fprintln(out, "Non-interactive installs run: skali cluster install --config node.yaml")
			return nil
		}
		return runInteractiveFreshFlow(ctx, out)
	default:
		status, err := installer.GatherStatus(ctx, runner())
		if err != nil {
			return err
		}
		printStatus(out, status)
		if !cliprompt.Interactive() {
			return nil
		}
		return runMenu(ctx, out, status)
	}
}

// unmanagedError words the adoption guard: a k3s host without a record is
// never adopted or destroyed.
func unmanagedError() error {
	return fmt.Errorf("k3s is installed but no skali installation record exists at %s; "+
		"this host is not managed by skali and will not be adopted or destroyed",
		installer.RecordPath)
}

// printFreshHeader renders the fresh/unmanaged host block.
func printFreshHeader(out *os.File, detected *installer.Host) {
	fmt.Fprintf(out, "host %s: %s\n", hostLabel(detected), detected.State)
	fmt.Fprintf(out, "  os      %s (linux/%s)\n", orUnknown(detected.OS), goArch(detected.Arch))
	if darwinInfo != nil {
		fmt.Fprintf(out, "  vm      %s (Lima, network %s)\n", darwinInfo.Instance, darwinInfo.Network)
	}
	if detected.K3sVersion != "" {
		fmt.Fprintf(out, "  k3s     %s (unmanaged)\n", detected.K3sVersion)
	} else {
		fmt.Fprintln(out, "  k3s     not installed")
	}
	fmt.Fprintln(out, "  record  none")
	fmt.Fprintln(out)
}

// printStatus renders the transcript section 6 status block.
func printStatus(out *os.File, status *installer.Status) {
	detected := status.Host
	record := detected.Record
	label := string(detected.State)
	if record != nil {
		switch detected.State {
		case installer.StateServer:
			label = fmt.Sprintf("Skali server (cluster %q)", record.Cluster)
		case installer.StateAgent:
			label = fmt.Sprintf("Skali agent (cluster %q)", record.Cluster)
		case installer.StateDamaged:
			label = fmt.Sprintf("damaged Skali installation (cluster %q)", record.Cluster)
		case installer.StateInterrupted:
			label = fmt.Sprintf("interrupted Skali installation (cluster %q)", record.Cluster)
		case installer.StateOrphaned:
			label = fmt.Sprintf("orphaned Skali installation (cluster %q)", record.Cluster)
		case installer.StateEnrolled:
			label = fmt.Sprintf("enrolled Skali candidate (cluster %q)", record.Cluster)
		}
	}
	if detected.State == installer.StateServer && healthyOverall(status) {
		label = "healthy " + label
	}
	fmt.Fprintf(out, "host %s: %s\n", hostLabel(detected), label)
	if darwinInfo != nil {
		fmt.Fprintf(out, "  vm         %s (Lima, network %s)\n", darwinInfo.Instance, darwinInfo.Network)
	}
	if record != nil && record.Lifecycle != nil && !record.InstallComplete() {
		fmt.Fprintf(out, "  install    %s at phase %s\n",
			record.Lifecycle.Status, record.Lifecycle.Phase)
		if record.Lifecycle.LastError != "" {
			fmt.Fprintf(out, "  error      %s\n", record.Lifecycle.LastError)
		}
		if record.Lifecycle.LastLog != "" {
			fmt.Fprintf(out, "  log        %s\n", record.Lifecycle.LastLog)
		}
	}

	k3sSuffix := "(expected " + installer.K3sVersion + ")"
	if status.K3sCurrent {
		k3sSuffix = "(current)"
	}
	fmt.Fprintf(out, "  k3s        %s %s\n", orUnknown(detected.K3sVersion), k3sSuffix)

	maintained := ""
	if status.InitOwner != "" {
		maintained = "; maintained on " + status.InitOwner
	}
	switch {
	case !status.Initialized:
		fmt.Fprintln(out, "  bundle     not initialized; run skali cluster init")
	case status.BundleCurrent:
		fmt.Fprintf(out, "  bundle     %s (current%s)\n", status.BundleVersion, maintained)
	default:
		fmt.Fprintf(out, "  bundle     %s (skali is %s%s)\n", status.BundleVersion, versionpkg.Version, maintained)
	}
	if status.Datastore == "sqlite" {
		fmt.Fprintln(out, "  datastore  sqlite (legacy; server join disabled, reinstall to enable ha)")
	}

	if status.ClusterReachable {
		if status.TierDrift() {
			fmt.Fprintf(out, "  tier       deployed %s, available %s (run skali cluster tier)\n",
				status.DeployedTier, status.AvailableTier)
		}
		fmt.Fprintf(out, "  nodes      %d joined (%s)\n", len(status.Nodes), nodeRoleCounts(status))
		servers := status.Servers()
		if servers > 0 && servers%2 == 0 {
			fmt.Fprintf(out, "  servers    %d (even count; etcd quorum prefers one or three)\n", servers)
		}
		for _, node := range status.Nodes {
			if !node.Current && node.K3sVersion != "" {
				fmt.Fprintf(out, "  node       %s %s (needs upgrade)\n", node.Name, node.K3sVersion)
			}
		}
		parts := make([]string, 0, len(status.Components))
		for _, component := range status.Components {
			if component.Healthy {
				parts = append(parts, component.Name+" healthy")
			} else {
				parts = append(parts, component.Name+" "+component.Detail)
			}
		}
		fmt.Fprintf(out, "  bootstrap  %s\n", strings.Join(parts, ", "))
	} else {
		fmt.Fprintln(out, "  cluster    kubernetes api unreachable")
	}
	for _, problem := range detected.Problems {
		fmt.Fprintf(out, "  problem    %s\n", problem)
	}
	if record != nil && record.Reconciled() {
		printReconciledStatus(out, status)
	}
	fmt.Fprintln(out)
}

func printReconciledStatus(out *os.File, status *installer.Status) {
	if status.Reconciled == nil {
		record := status.Host.Record
		if status.Host.State == installer.StateEnrolled {
			fmt.Fprintln(out, "  enrollment awaiting cluster apply")
			if record.Coordinator != nil &&
				len(record.Coordinator.Endpoints) > 0 {
				fmt.Fprintf(out, "  coordinator %s\n",
					record.Coordinator.Endpoints[0])
			}
		} else if status.CoordinatorError != "" {
			fmt.Fprintf(out, "  coordinator unavailable: %s\n", status.CoordinatorError)
		}
		if record != nil && record.Coordinator != nil {
			cache := record.Coordinator
			if cache.ConvergedRevision != "" {
				fmt.Fprintf(out, "  cached      converged %s\n",
					shortRevision(cache.ConvergedRevision))
			}
			if cache.TargetRevision != "" {
				fmt.Fprintf(out, "  cached      target %s\n",
					shortRevision(cache.TargetRevision))
			}
			if cache.CandidateRevision != "" {
				fmt.Fprintf(out, "  cached      candidate %s\n",
					shortRevision(cache.CandidateRevision))
			}
			if cache.LastOperation != "" {
				fmt.Fprintf(out, "  cached      operation %s (%s)\n",
					shortRevision(cache.LastOperation), cache.LastOperationPhase)
			}
		}
		return
	}
	state := status.Reconciled
	if state.ReconciliationPaused {
		fmt.Fprintln(out, "  reconcile  paused for recovery")
	}
	fmt.Fprintf(out, "  revision   converged %s\n", shortRevision(state.ConvergedRevision))
	if state.TargetRevision != "" {
		fmt.Fprintf(out, "  target     %s\n", shortRevision(state.TargetRevision))
	}
	fmt.Fprintf(out, "  candidate  %s\n", shortRevision(state.CandidateRevision))
	if plan, err := candidatePlan(state, false); err == nil && !plan.Empty() {
		fmt.Fprintf(out, "  changes    %d pending action(s); run skali cluster plan\n",
			len(plan.Actions))
	}
	if state.CurrentOperation != "" {
		if operation, ok := state.Operations[state.CurrentOperation]; ok {
			fmt.Fprintf(out, "  operation  %s (%s)\n",
				shortRevision(operation.ID), operation.Phase)
			for _, node := range clusterstate.SortedNodes(state.Nodes) {
				step, exists := operation.NodeSteps[node.ID]
				if !exists {
					continue
				}
				fmt.Fprintf(out, "  progress   %-20s %-12s %s\n",
					node.Name, step.Action, step.Phase)
				if step.LastError != "" {
					fmt.Fprintf(out, "  error      %s: %s\n", node.Name, step.LastError)
				}
			}
			if operation.Phase == clusterstate.OperationFailed {
				fmt.Fprintln(out, "  recovery   skali cluster diagnose")
				fmt.Fprintln(out, "  recovery   skali cluster apply --yes")
			}
		}
	}
	now := time.Now()
	for _, node := range clusterstate.SortedNodes(state.Nodes) {
		heartbeat := "never"
		if !node.LastSeen.IsZero() {
			heartbeat = now.Sub(node.LastSeen).Round(time.Second).String() + " ago"
		}
		fmt.Fprintf(out, "  managed    %-20s %-7s %-22s heartbeat %s\n",
			node.Name, node.Role, node.Phase, heartbeat)
		if node.Phase == clusterstate.NodePhaseAwaitingCleanup {
			fmt.Fprintf(out, "  recovery   skali cluster node forget %s --force  (only if the host is unreachable)\n",
				node.Name)
		}
	}
}

func shortRevision(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func runRecoveryMenu(ctx context.Context, out *os.File, status *installer.Status) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprintln(out, "recovery options")
	choice, err := cliprompt.Select(reader, out, "  : ", []string{
		"resume/edit inputs",
		"diagnose",
		"repair",
		"uninstall",
		"quit",
	}, -1)
	if err != nil {
		return err
	}
	switch choice {
	case 0:
		return runInteractiveResume(ctx, out, reader, status.Host)
	case 1:
		diagnosis, err := installer.Diagnose(ctx, runner(), installer.DiagnoseOptions{})
		if err != nil {
			return err
		}
		printDiagnosis(out, diagnosis)
		if diagnosis.Fails() > 0 {
			return fmt.Errorf("diagnosis found %d problem(s)", diagnosis.Fails())
		}
		return nil
	case 2:
		if status.Host.State == installer.StateOrphaned {
			if !cliprompt.Confirm(reader, "Recover ownership of this interrupted Skali install before repair? [y/N] ") {
				return errors.New("recovery was not confirmed; nothing was changed")
			}
			if err := installer.PersistOrphanRecord(ctx, runner(), status.Host.Record); err != nil {
				return err
			}
		}
		return runRepairFlow(ctx, out, reader, false)
	case 3:
		return runUninstallFlow(ctx, out, reader, "", "")
	default:
		return nil
	}
}

func runInteractiveResume(ctx context.Context, out *os.File, reader *bufio.Reader, detected *installer.Host) error {
	record := detected.Record
	if record == nil {
		return errors.New("the interrupted installation has no recoverable inputs")
	}
	if record.Reconciled() && record.EnrolledOnly() {
		tokenFile, err := cliprompt.Line(reader,
			"  enrollment token file path (empty to paste the token): ")
		if err != nil {
			return err
		}
		token := ""
		if tokenFile == "" {
			token, err = cliprompt.Secret(reader, "  enrollment token: ")
		} else {
			data, readErr := os.ReadFile(tokenFile)
			if readErr != nil {
				return readErr
			}
			token = strings.TrimSpace(string(data))
		}
		if err != nil {
			return err
		}
		endpoint := ""
		if record.Coordinator != nil && len(record.Coordinator.Endpoints) > 0 {
			endpoint = record.Coordinator.Endpoints[0]
		}
		endpoint, err = cliprompt.LineDefault(reader,
			"  coordinator ["+endpoint+"]: ", endpoint)
		if err != nil {
			return err
		}
		resumed, err := runReconciledEnrollment(ctx, reconciledEnrollmentOptions{
			Server: endpoint, Token: token,
			Capabilities: append([]string(nil), record.Node.Capabilities...),
			NodeIP:       record.Node.IP,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "node %s resumed enrollment; pending cluster apply\n", resumed.Node.Name)
		return nil
	}
	if record.Reconciled() && record.Node.Role == layout.RoleServer && record.Join == nil {
		hostdBinary, _, err := loadHostdBinary()
		if err != nil {
			return err
		}
		if err := bootstrapReconciledSeed(ctx, record, hostdBinary); err != nil {
			return err
		}
		fmt.Fprintln(out, "seed coordinator bootstrap completed")
		return nil
	}
	opts := installer.InstallOptions{
		Cluster:       record.Cluster,
		Role:          record.Node.Role,
		Capabilities:  append([]string(nil), record.Node.Capabilities...),
		Endpoints:     record.Endpoints,
		TLS:           record.TLS,
		RecoverOrphan: detected.State == installer.StateOrphaned,
	}
	if record.Join != nil {
		tokenFile, err := cliprompt.Line(reader, "  join token file path (empty to paste the token): ")
		if err != nil {
			return err
		}
		token := ""
		if tokenFile == "" {
			token, err = cliprompt.Secret(reader, "  join token: ")
			if err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(tokenFile)
			if err != nil {
				return fmt.Errorf("read join token file %s: %w", tokenFile, err)
			}
			token = strings.TrimSpace(string(data))
		}
		claims, err := installer.InspectJoinToken(token)
		if err != nil {
			return err
		}
		serverDefault := record.Join.Server
		if claims.Server != "" {
			serverDefault = claims.Server
		}
		server, err := cliprompt.LineDefault(reader, "  server url ["+serverDefault+"]: ", serverDefault)
		if err != nil {
			return err
		}
		opts.Join = &installer.JoinOptions{Server: server, Token: token}
	}
	fmt.Fprintln(out)
	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)
	opts.Progress = progress
	if err := applyDarwinInstallOptions(ctx, &opts); err != nil {
		progress.Abort()
		return err
	}
	_, err := installer.Install(ctx, runner(), opts)
	if err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintf(out, "\nThis node completed its installation in cluster %q as a %s.\n",
		record.Cluster, record.Node.Role)
	return nil
}

// nodeRoleCounts words the nodes-line suffix: "1 server", "2 servers,
// 1 agent".
func nodeRoleCounts(status *installer.Status) string {
	servers := status.Servers()
	agents := len(status.Nodes) - servers
	parts := []string{pluralCount(servers, "server")}
	if agents > 0 {
		parts = append(parts, pluralCount(agents, "agent"))
	}
	return strings.Join(parts, ", ")
}

func pluralCount(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

func healthyOverall(status *installer.Status) bool {
	if !status.ClusterReachable || !status.Initialized || len(status.Components) == 0 {
		return false
	}
	for _, component := range status.Components {
		if !component.Healthy {
			return false
		}
	}
	return true
}

// runMenu is the deliberately dumb maintenance loop: numbered dispatch,
// re-render, and named refusals for operations of later slices. Tier
// drift switches the first prompt to the verbose menu naming the pending
// tier change.
func runMenu(ctx context.Context, out *os.File, status *installer.Status) error {
	reader := bufio.NewReader(os.Stdin)
	if healthyOverall(status) && status.BundleCurrent && status.K3sCurrent && !status.TierDrift() {
		fmt.Fprintln(out, "nothing to do")
	}
	prompt := "  [1] status  [2] apply tier  [3] upgrade  [4] repair  [5] uninstall  [q] quit\n  : "
	if status.TierDrift() {
		printTierDriftBlock(out, status)
		prompt = verboseMenu(status) + "  : "
	}
	for {
		answer, err := cliprompt.Line(reader, prompt)
		if err != nil {
			return nil
		}
		prompt = "  [1] status  [2] apply tier  [3] upgrade  [4] repair  [5] uninstall  [q] quit\n  : "
		switch strings.ToLower(answer) {
		case "1":
			refreshed, err := installer.GatherStatus(ctx, runner())
			if err != nil {
				return err
			}
			printStatus(out, refreshed)
		case "2":
			return runTierFlow(ctx, out, reader, false)
		case "3":
			return runUpgradeFlow(ctx, out, reader, false)
		case "4":
			return runRepairFlow(ctx, out, reader, false)
		case "5":
			return runUninstallFlow(ctx, out, reader, "", "")
		case "q", "quit", "":
			return nil
		default:
			fmt.Fprintln(out, "please answer 1-5 or q")
		}
	}
}

// printTierDriftBlock renders the pending tier change ahead of the menu.
func printTierDriftBlock(out *os.File, status *installer.Status) {
	fmt.Fprintf(out, "  database nodes    %d (%s)\n",
		len(status.DatabaseNodes), strings.Join(status.DatabaseNodes, ", "))
	fmt.Fprintf(out, "  deployed tier     %s\n", status.DeployedTier)
	fmt.Fprintf(out, "  available tier    %s\n", status.AvailableTier)
	fmt.Fprintln(out)
}

// verboseMenu describes each entry; shown when a tier change is pending
// so entry [2] names what applying it would do.
func verboseMenu(status *installer.Status) string {
	tierAction := "upgrade system databases to " + string(status.AvailableTier)
	if bundleTierRank(status.AvailableTier) < bundleTierRank(status.DeployedTier) {
		tierAction = "downgrade system databases to " + string(status.AvailableTier)
	}
	return "" +
		"  [1] status        show installation health\n" +
		"  [2] apply tier    " + tierAction + "\n" +
		"  [3] upgrade       k3s / bundle versions\n" +
		"  [4] repair        diagnose and repair\n" +
		"  [5] uninstall     scoped removal\n"
}

func bundleTierRank(tier layout.Tier) int {
	switch tier {
	case layout.TierSynchronous:
		return 3
	case layout.TierAsynchronous:
		return 2
	default:
		return 1
	}
}

func hostLabel(detected *installer.Host) string {
	if detected.Hostname != "" {
		return detected.Hostname
	}
	return "(unknown)"
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// goArch maps uname machine names onto Go/OCI architecture names.
func goArch(machine string) string {
	switch machine {
	case "aarch64", "arm64":
		return "arm64"
	case "x86_64", "amd64":
		return "amd64"
	default:
		return machine
	}
}
