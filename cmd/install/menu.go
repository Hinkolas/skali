package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/installer"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// runRoot is the bare `skali-installer` entry: detect, render the state
// header, and offer the maintenance menu interactively. Detection is
// read-only; no maintenance action runs without being selected.
func runRoot(cmd *cobra.Command) error {
	ctx := cmd.Context()
	out := os.Stdout
	banner(out)

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
	case installer.StateFresh:
		printFreshHeader(out, detected)
		if !cliprompt.Interactive() {
			fmt.Fprintln(out, "This host is not part of a Skali installation.")
			fmt.Fprintln(out, "Non-interactive installs run: skali-installer install --config node.yaml")
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
		"this host is not managed by skali-installer and will not be adopted or destroyed",
		installer.RecordPath)
}

// printFreshHeader renders the fresh/unmanaged host block.
func printFreshHeader(out *os.File, detected *installer.Host) {
	fmt.Fprintf(out, "host %s: %s\n", hostLabel(detected), detected.State)
	fmt.Fprintf(out, "  os      %s (linux/%s)\n", orUnknown(detected.OS), goArch(detected.Arch))
	if detected.K3sVersion != "" {
		fmt.Fprintf(out, "  k3s     %s (unmanaged)\n", detected.K3sVersion)
	} else {
		fmt.Fprintln(out, "  k3s     not installed")
	}
	fmt.Fprintln(out, "  record  none")
	fmt.Fprintln(out)
}

// printStatus renders the transcript section 5 status block.
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
		}
	}
	if detected.State == installer.StateServer && healthyOverall(status) {
		label = "healthy " + label
	}
	fmt.Fprintf(out, "host %s: %s\n", hostLabel(detected), label)

	k3sSuffix := "(expected " + installer.K3sVersion + ")"
	if status.K3sCurrent {
		k3sSuffix = "(current)"
	}
	fmt.Fprintf(out, "  k3s        %s %s\n", orUnknown(detected.K3sVersion), k3sSuffix)

	switch {
	case !status.Initialized:
		fmt.Fprintln(out, "  bundle     not initialized; run skali-installer init")
	case status.BundleCurrent:
		fmt.Fprintf(out, "  bundle     %s (current)\n", status.BundleVersion)
	default:
		fmt.Fprintf(out, "  bundle     %s (installer is %s)\n", status.BundleVersion, versionpkg.Version)
	}

	if status.ClusterReachable {
		fmt.Fprintf(out, "  nodes      %d joined\n", status.Nodes)
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
	fmt.Fprintln(out)
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
// re-render, and named refusals for operations of later slices.
func runMenu(ctx context.Context, out *os.File, status *installer.Status) error {
	reader := bufio.NewReader(os.Stdin)
	if healthyOverall(status) && status.BundleCurrent && status.K3sCurrent {
		fmt.Fprintln(out, "nothing to do")
	}
	for {
		answer, err := cliprompt.Line(reader,
			"  [1] status  [2] apply tier  [3] upgrade  [4] repair  [5] uninstall  [q] quit\n  : ")
		if err != nil {
			return nil
		}
		switch strings.ToLower(answer) {
		case "1":
			refreshed, err := installer.GatherStatus(ctx, runner())
			if err != nil {
				return err
			}
			printStatus(out, refreshed)
		case "2":
			fmt.Fprintln(out, "not implemented in this slice: tier changes arrive with a later milestone")
		case "3":
			fmt.Fprintln(out, "not implemented in this slice: upgrades arrive with a later milestone")
		case "4":
			fmt.Fprintln(out, "not implemented in this slice: diagnose and repair arrive with a later milestone")
		case "5":
			return runUninstallFlow(ctx, out, reader, "", "")
		case "q", "quit", "":
			return nil
		default:
			fmt.Fprintln(out, "please answer 1-5 or q")
		}
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
