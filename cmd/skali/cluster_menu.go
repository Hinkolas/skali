package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/utils"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// runClusterRoot is the bare `skali cluster` entry: detect, render the state
// header, and offer the maintenance menu interactively. Detection is
// read-only; no maintenance action runs without being selected.
func runClusterRoot(cmd *cobra.Command) error {
	ctx := cmd.Context()
	out := os.Stdout

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
	case installer.StateInterrupted, installer.StateOrphaned, installer.StateEnrolled:
		status, err := installer.GatherStatus(ctx, runner())
		if err != nil {
			return err
		}
		printStatus(out, status)
		if !cliprompt.Interactive() {
			return nil
		}
		if detected.State == installer.StateEnrolled && detected.Record.Lifecycle != nil && detected.Record.Lifecycle.Status == installer.InstallStatusEnrolled && detected.Record.Lifecycle.Phase == installer.InstallPhaseAwaitingApply {
			fmt.Fprintln(out, "Review skali cluster plan on an active controller, then apply or initialize the cluster.")
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
		// Interactive runs reach the status block through the menu; a
		// non-interactive run has no menu, so the block is the output.
		if !cliprompt.Interactive() {
			printStatus(out, status)
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
	style := clirender.StyleFor(out)
	fmt.Fprintf(out, "%s %s\n",
		style.BrightCyan("◆"),
		style.Bold(hostLabel(detected)))
	printStatusRow(out, style, "status", style.BrightYellow(string(detected.State)))
	printStatusRow(out, style, "os",
		fmt.Sprintf("%s (linux/%s)", orUnknown(detected.OS), goArch(detected.Arch)))
	if darwinInfo != nil {
		printStatusRow(out, style, "vm",
			fmt.Sprintf("%s (Lima, network %s)", darwinInfo.Instance, darwinInfo.Network))
	}
	if detected.K3sVersion != "" {
		printStatusRow(out, style, "k3s",
			fmt.Sprintf("%s %s", detected.K3sVersion, style.BrightYellow("(unmanaged)")))
	} else {
		printStatusRow(out, style, "k3s", style.Muted("not installed"))
	}
	printStatusRow(out, style, "record", style.Muted("none"))
	fmt.Fprintln(out)
}

// printStatus renders the status block.
func printStatus(out *os.File, status *installer.Status, all ...bool) {
	detected := status.Host
	record := detected.Record
	style := clirender.StyleFor(out)
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
	fmt.Fprintf(out, "%s %s\n",
		style.BrightCyan("◆"),
		style.Bold(hostLabel(detected)))
	if detected.State == installer.StateServer && healthyOverall(status) {
		label = style.BrightGreen("healthy") + " · " + label
	}
	printStatusRow(out, style, "status", label)
	if darwinInfo != nil {
		printStatusRow(out, style, "vm",
			fmt.Sprintf("%s (Lima, network %s)", darwinInfo.Instance, darwinInfo.Network))
	}
	if record != nil && record.Lifecycle != nil && !record.InstallComplete() {
		printStatusRow(out, style, "install",
			fmt.Sprintf("%s at phase %s", record.Lifecycle.Status, record.Lifecycle.Phase))
		if record.Lifecycle.LastError != "" {
			printStatusRow(out, style, "error", style.Red(record.Lifecycle.LastError))
		}
		if record.Lifecycle.LastLog != "" {
			printStatusRow(out, style, "log", record.Lifecycle.LastLog)
		}
	}

	// The addresses are worth a line of their own: on a multi-homed host
	// they decide which network carries cluster traffic and enrollment.
	if record != nil && record.Node.IP != "" {
		printStatusRow(out, style, "addresses", describeNetwork(record.Node.Network()))
	}

	k3sSuffix := style.BrightYellow("(expected " + installer.K3sVersion + ")")
	if status.K3sCurrent {
		k3sSuffix = style.BrightGreen("(current)")
	}
	printStatusRow(out, style, "k3s",
		fmt.Sprintf("%s %s", orUnknown(detected.K3sVersion), k3sSuffix))

	maintained := ""
	if status.InitOwner != "" {
		maintained = "; maintained on " + status.InitOwner
	}
	switch {
	case !status.Initialized:
		printStatusRow(out, style, "bundle",
			style.BrightYellow("not initialized")+"; run skali cluster init")
	case status.BundleCurrent:
		printStatusRow(out, style, "bundle",
			fmt.Sprintf("%s %s", status.BundleVersion,
				style.BrightGreen("(current"+maintained+")")))
	default:
		printStatusRow(out, style, "bundle",
			fmt.Sprintf("%s %s", status.BundleVersion,
				style.BrightYellow("(skali is "+versionpkg.Version+maintained+")")))
	}
	if status.Datastore == "sqlite" {
		printStatusRow(out, style, "datastore",
			style.BrightYellow("sqlite (legacy; server join disabled, reinstall to enable ha)"))
	}

	if status.ClusterReachable {
		if status.TierDrift() {
			printStatusRow(out, style, "tier",
				style.BrightYellow(fmt.Sprintf("deployed %s, available %s (run skali cluster tier)",
					status.DeployedTier, status.AvailableTier)))
		}
		printStatusRow(out, style, "nodes",
			fmt.Sprintf("%d joined (%s)", len(status.Nodes), nodeRoleCounts(status)))
		if archs := nodeArchCounts(status); len(archs) > 1 {
			printStatusRow(out, style, "platforms", strings.Join(archs, ", "))
			if record == nil || len(record.PlatformPreference) == 0 {
				printStatusRow(out, style, "",
					style.Dim("mixed architectures build multi-arch images; prefer one with "+
						"skali cluster init --platform-preference"))
			} else {
				printStatusRow(out, style, "preference",
					strings.Join(record.PlatformPreference, ", "))
			}
		}
		servers := status.Servers()
		if servers > 0 && servers%2 == 0 {
			printStatusRow(out, style, "servers",
				style.BrightYellow(fmt.Sprintf("%d (even count; etcd quorum prefers one or three)", servers)))
		}
		for _, node := range status.Nodes {
			if !node.Current && node.K3sVersion != "" {
				printStatusRow(out, style, "node",
					fmt.Sprintf("%s %s %s", node.Name, node.K3sVersion,
						style.BrightYellow("(needs upgrade)")))
			}
		}
		parts := make([]string, 0, len(status.Components))
		for _, component := range status.Components {
			if component.Healthy {
				// The healthy detail carries information beyond the flag,
				// e.g. the database's deployed tier.
				parts = append(parts, component.Name+" "+style.BrightGreen(component.Detail))
			} else {
				parts = append(parts, component.Name+" "+style.BrightYellow(component.Detail))
			}
		}
		printStatusRow(out, style, "bootstrap", strings.Join(parts, ", "))
	} else if detected.State != installer.StateEnrolled {
		printStatusRow(out, style, "cluster", style.Red("kubernetes api unreachable"))
	}
	for _, problem := range detected.Problems {
		printStatusRow(out, style, "problem", style.Red(problem))
	}
	if record != nil && record.Reconciled() {
		printReconciledStatus(out, status, all...)
	}
	fmt.Fprintln(out)
}

func printStatusRow(out *os.File, style *clirender.Style, label, value string) {
	fmt.Fprintf(out, "  %s %s\n", style.Muted(fmt.Sprintf("%-10s", label)), value)
}

func printReconciledStatus(out *os.File, status *installer.Status, all ...bool) {
	style := clirender.StyleFor(out)
	if status.Reconciled == nil {
		record := status.Host.Record
		if status.Host.State == installer.StateEnrolled {
			printStatusRow(out, style, "enrollment",
				style.BrightYellow(enrollmentStatus(record)))
			if record.Coordinator != nil &&
				len(record.Coordinator.Endpoints) > 0 {
				printStatusRow(out, style, "coordinator",
					record.Coordinator.Endpoints[0])
			}
		} else if status.CoordinatorError != "" {
			printStatusRow(out, style, "coordinator",
				style.Red("unavailable: "+status.CoordinatorError))
		}
		if record != nil && record.Coordinator != nil {
			cache := record.Coordinator
			if cache.ConvergedRevision != "" {
				printStatusRow(out, style, "cached",
					"converged "+shortRevision(cache.ConvergedRevision))
			}
			if cache.TargetRevision != "" {
				printStatusRow(out, style, "cached",
					"target "+shortRevision(cache.TargetRevision))
			}
			if cache.CandidateRevision != "" {
				printStatusRow(out, style, "cached",
					"candidate "+shortRevision(cache.CandidateRevision))
			}
			if cache.LastOperation != "" {
				printStatusRow(out, style, "cached",
					fmt.Sprintf("operation %s (%s)",
						shortRevision(cache.LastOperation), cache.LastOperationPhase))
			}
		}
		return
	}
	state := status.Reconciled
	if state.ReconciliationPaused {
		printStatusRow(out, style, "reconcile",
			style.BrightYellow("paused for recovery"))
	}
	printStatusRow(out, style, "revision",
		"converged "+shortRevision(state.ConvergedRevision))
	if state.TargetRevision != "" {
		printStatusRow(out, style, "target", shortRevision(state.TargetRevision))
	}
	printStatusRow(out, style, "candidate", shortRevision(state.CandidateRevision))
	if plan, err := candidatePlan(state, false); err == nil && !plan.Empty() {
		printStatusRow(out, style, "changes",
			style.BrightYellow(fmt.Sprintf("%d pending action(s); run skali cluster plan",
				len(plan.Actions))))
	}
	if state.CurrentOperation != "" {
		if operation, ok := state.Operations[state.CurrentOperation]; ok {
			printStatusRow(out, style, "operation",
				fmt.Sprintf("%s (%s)", shortRevision(operation.ID), operation.Phase))
			for _, node := range clusterstate.SortedNodes(state.Nodes) {
				step, exists := operation.NodeSteps[node.ID]
				if !exists {
					continue
				}
				printStatusRow(out, style, "progress",
					fmt.Sprintf("%-20s %-12s %s", node.Name, step.Action, step.Phase))
				if step.LastError != "" {
					printStatusRow(out, style, "error",
						style.Red(node.Name+": "+step.LastError))
				}
			}
			if operation.Phase == clusterstate.OperationFailed {
				printStatusRow(out, style, "recovery", "skali cluster diagnose")
				printStatusRow(out, style, "recovery", "skali cluster apply --yes")
			}
		}
	}
	now := time.Now()
	nodes := clusterstate.SortedNodes(state.Nodes)
	if len(nodes) > 0 {
		fmt.Fprintln(out, "  "+style.Muted("managed nodes"))
	}
	for _, node := range nodes {
		if (len(all) == 0 || !all[0]) && (node.Phase == clusterstate.NodePhaseRemoved || node.Phase == clusterstate.NodePhaseCancelled) {
			continue
		}
		heartbeat := "never"
		if !node.LastSeen.IsZero() {
			heartbeat = now.Sub(node.LastSeen).Round(time.Second).String() + " ago"
		}
		phase := fmt.Sprintf("%-22s", node.Phase)
		if node.Phase == clusterstate.NodePhaseActive {
			phase = style.BrightGreen(phase)
		}
		fmt.Fprintf(out, "    %-20s %-7s %s %s\n",
			node.Name, node.Role, phase, style.Muted("heartbeat "+heartbeat))
		if node.Phase == clusterstate.NodePhaseAwaitingCleanup {
			printStatusRow(out, style, "recovery",
				fmt.Sprintf("skali cluster node forget %s --force  (only if the host is unreachable)",
					node.Name))
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
	resumeLabel := "Resume or edit inputs"
	if status.Host.Record != nil && status.Host.Record.EnrolledOnly() {
		resumeLabel = "Resume enrollment"
	}
	choice, err := promptSession(out, reader).Select(ctx, cliprompt.SelectOptions{
		Title:       "How should Skali recover this installation?",
		Description: "Diagnosis is read-only; other actions may ask for confirmation.",
		Options: []cliprompt.Option{
			{Label: resumeLabel, Value: "resume"},
			{Label: "Diagnose", Value: "diagnose"},
			{Label: "Repair", Value: "repair"},
			{Label: "Uninstall", Value: "uninstall"},
			{Label: "Quit", Value: "quit"},
		},
		DefaultValue: "quit",
	})
	if err != nil {
		return err
	}
	switch choice {
	case "resume":
		return runInteractiveResume(ctx, out, reader, status.Host)
	case "diagnose":
		diagnosis, err := installer.Diagnose(ctx, runner(), installer.DiagnoseOptions{})
		if err != nil {
			return err
		}
		printDiagnosis(out, diagnosis)
		if diagnosis.Fails() > 0 {
			return fmt.Errorf("diagnosis found %d problem(s)", diagnosis.Fails())
		}
		return nil
	case "repair":
		if status.Host.State == installer.StateOrphaned {
			confirmed, err := promptSession(out, reader).Confirm(ctx, cliprompt.ConfirmOptions{
				Title: "Recover ownership of this interrupted Skali install before repair?",
			})
			if err != nil {
				return err
			}
			if !confirmed {
				return errors.New("recovery was not confirmed; nothing was changed")
			}
			if err := installer.PersistOrphanRecord(ctx, runner(), status.Host.Record); err != nil {
				return err
			}
		}
		return runRepairFlow(ctx, out, reader, false)
	case "uninstall":
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
		resumed, err := runReconciledEnrollment(ctx, reconciledEnrollmentOptions{Interactive: true})
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
			token, err = promptJoinToken(ctx, reader)
			if err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(tokenFile)
			if err != nil {
				return fmt.Errorf("read join token file %s: %w", tokenFile, err)
			}
			token = clusterstate.NormalizeToken(string(data))
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
	parts := []string{utils.PluralCount(servers, "server")}
	if agents > 0 {
		parts = append(parts, utils.PluralCount(agents, "agent"))
	}
	return strings.Join(parts, ", ")
}

// nodeArchCounts summarizes the cluster's CPU architectures, e.g.
// ["2 arm64", "1 amd64"], sorted by count descending then name. A single
// entry means a homogeneous cluster.
func nodeArchCounts(status *installer.Status) []string {
	counts := map[string]int{}
	for _, node := range status.Nodes {
		if node.Arch != "" {
			counts[node.Arch]++
		}
	}
	archs := utils.SortedKeys(counts)
	sort.SliceStable(archs, func(i, j int) bool { return counts[archs[i]] > counts[archs[j]] })
	parts := make([]string, 0, len(archs))
	for _, arch := range archs {
		parts = append(parts, fmt.Sprintf("%d %s", counts[arch], arch))
	}
	return parts
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
// re-render, and named refusals for operations of later slices. The status
// block belongs to the status entry alone, so anything pending arrives
// here as a one-line description on the first prompt instead.
func runMenu(ctx context.Context, out *os.File, status *installer.Status) error {
	reader := bufio.NewReader(os.Stdin)
	description := ""
	switch {
	case status.TierDrift():
		printTierDriftBlock(out, status)
		description = fmt.Sprintf("Database tier change pending: %s to %s.",
			status.DeployedTier, status.AvailableTier)
	case status.Host.State != installer.StateServer:
		// Agents and candidates read local state alone: they reach no
		// Kubernetes API and own no bundle, so health and currency are
		// not theirs to claim.
	case !healthyOverall(status):
		description = "This installation is not healthy; show status for details."
	case !status.K3sCurrent || !status.BundleCurrent:
		description = "An upgrade is pending; show status for details."
	default:
		fmt.Fprintln(out, "nothing to do")
	}
	for {
		answer, err := promptSession(out, reader).Select(ctx, cliprompt.SelectOptions{
			Title:       "What would you like to do?",
			Description: description,
			Options: []cliprompt.Option{
				{Label: "Show status", Value: "status"},
				{Label: "Apply database tier", Value: "tier"},
				{Label: "Upgrade", Value: "upgrade"},
				{Label: "Repair", Value: "repair"},
				{Label: "Uninstall", Value: "uninstall"},
				{Label: "Quit", Value: "quit"},
			},
			DefaultValue: "quit",
		})
		if err != nil {
			return nil
		}
		description = ""
		switch answer {
		case "status":
			refreshed, err := installer.GatherStatus(ctx, runner())
			if err != nil {
				return err
			}
			printStatus(out, refreshed)
		case "tier":
			return runTierFlow(ctx, out, reader, false)
		case "upgrade":
			return runUpgradeFlow(ctx, out, reader, false)
		case "repair":
			return runRepairFlow(ctx, out, reader, false)
		case "uninstall":
			return runUninstallFlow(ctx, out, reader, "", "")
		case "quit":
			return nil
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
	if layout.TierInstances(status.AvailableTier) < layout.TierInstances(status.DeployedTier) {
		tierAction = "downgrade system databases to " + string(status.AvailableTier)
	}
	return "" +
		"  [1] status        show installation health\n" +
		"  [2] apply tier    " + tierAction + "\n" +
		"  [3] upgrade       k3s / bundle versions\n" +
		"  [4] repair        diagnose and repair\n" +
		"  [5] uninstall     scoped removal\n"
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

func enrollmentStatus(record *installer.Record) string {
	if record != nil && record.Lifecycle != nil && record.Lifecycle.Status == installer.InstallStatusFailed {
		return "enrollment incomplete; resume with sudo skali cluster join"
	}
	if record != nil && record.Coordinator != nil && !record.Coordinator.LastHeartbeatAt.IsZero() {
		if time.Since(record.Coordinator.LastHeartbeatAt) < 30*time.Second {
			return "agent connected; awaiting cluster apply"
		}
		return "awaiting cluster apply; coordinator heartbeat is stale"
	}
	return "enrolled; waiting for first agent heartbeat"
}
