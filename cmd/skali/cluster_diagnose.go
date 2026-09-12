package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/installer"
)

func newClusterDiagnoseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose",
		Short: "Diagnose this installation from host state and the Kubernetes API",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := os.Stdout
			if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
				return err
			}
			diagnosis, err := runDiagnosis(command, out)
			if err != nil {
				return err
			}
			if fails := diagnosis.Fails(); fails > 0 {
				return fmt.Errorf("diagnosis found %d problem(s)", fails)
			}
			return nil
		},
	}
}

// runDiagnosis guards the host state, runs the read-only diagnosis, and
// prints the full report. The report always prints completely before any
// exit-code decision.
func runDiagnosis(command *cobra.Command, out *os.File) (*installer.Diagnosis, error) {
	ctx := command.Context()
	detected, err := installer.Detect(ctx, runner())
	if err != nil {
		return nil, err
	}
	switch detected.State {
	case installer.StateServer, installer.StateAgent, installer.StateDamaged,
		installer.StateInterrupted, installer.StateOrphaned, installer.StateUnmanaged:
	default:
		return nil, fmt.Errorf("nothing to diagnose on this host (state %s); run skali cluster install first",
			detected.State)
	}

	diagnosis, err := installer.Diagnose(ctx, runner(), installer.DiagnoseOptions{})
	if err != nil {
		return nil, err
	}
	printDiagnosis(out, diagnosis)
	return diagnosis, nil
}

// printDiagnosis renders the diagnosis: the state
// header, one line per check with pod drilldown indented, then the
// deduplicated suggested actions.
func printDiagnosis(out io.Writer, diagnosis *installer.Diagnosis) {
	detected := diagnosis.Host
	label := string(detected.State)
	if detected.Record != nil {
		switch detected.State {
		case installer.StateServer:
			label = fmt.Sprintf("Skali server (cluster %q)", detected.Record.Cluster)
		case installer.StateAgent:
			label = fmt.Sprintf("Skali agent (cluster %q)", detected.Record.Cluster)
		case installer.StateDamaged:
			label = fmt.Sprintf("damaged Skali installation (cluster %q)", detected.Record.Cluster)
		case installer.StateInterrupted:
			label = fmt.Sprintf("interrupted Skali installation (cluster %q)", detected.Record.Cluster)
		case installer.StateOrphaned:
			label = fmt.Sprintf("orphaned Skali installation (cluster %q)", detected.Record.Cluster)
		}
	}
	fmt.Fprintf(out, "host %s: %s\n", hostLabel(detected), label)
	for _, check := range diagnosis.Checks {
		marker := "ok  "
		switch check.Severity {
		case installer.SeverityWarn:
			marker = "warn"
		case installer.SeverityFail:
			marker = "fail"
		}
		fmt.Fprintf(out, "  %s  %s: %s\n", marker, check.Name, check.Detail)
		for _, sub := range check.Sub {
			for line := range strings.SplitSeq(sub, "\n") {
				fmt.Fprintf(out, "          %s\n", strings.TrimPrefix(line, "  "))
			}
		}
	}
	if len(diagnosis.Suggestions) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "suggested action")
		for _, suggestion := range diagnosis.Suggestions {
			fmt.Fprintf(out, "  %s\n", suggestion)
		}
	}
}
