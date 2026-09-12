package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/utils"
)

func newRunCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "run",
		Short: "List, inspect, attach to, or cancel runs",
		Long: "Runs are the journal of platform operations: deployments, rollbacks,\n" +
			"and teardowns. This group lists and inspects them. For running a\n" +
			"project command (seed, migrate) with the application's environment,\n" +
			"see skali dev run.",
	}
	var remote string
	command.PersistentFlags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")

	var environment string
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the environment's runs",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(command.Context(), start, environment, remote)
			if err != nil {
				return err
			}
			runs, err := target.api.ListRuns(command.Context(), target.environmentID)
			if err != nil {
				return err
			}
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			fmt.Fprintf(out, "%s  %s %s\n\n", style.Dim("environment"),
				target.environment, style.Dim("("+target.remoteName+")"))
			fmt.Fprintf(out, "%-36s  %-11s  %-9s  %s\n", "RUN", "KIND", "STATUS", "STARTED")
			for _, run := range runs {
				started := ""
				if run.StartedAt != nil {
					started = utils.HumanSince(*run.StartedAt)
				}
				note := ""
				if run.BypassProtection {
					note = "  " + style.Yellow("bypassed protection")
				}
				fmt.Fprintf(out, "%-36s  %-11s  %-9s  %s%s\n", run.ID, run.Kind, run.Status, started, note)
			}
			return nil
		},
	}
	list.Flags().StringVar(&environment, "environment", "",
		"environment name (default: the checkout binding)")
	command.AddCommand(list)

	show := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Print a run's step tree",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			tree, err := api.GetRun(command.Context(), args[0])
			if err != nil {
				return err
			}
			out := command.OutOrStdout()
			// A one-shot renderer: styled glyphs on a terminal, the plain
			// transcript through a pipe; no rewriting either way.
			renderer := &clirender.Renderer{Out: out, Style: clirender.StyleFor(out)}
			renderer.Render(tree)
			return nil
		},
	}

	attach := &cobra.Command{
		Use:   "attach <run-id>",
		Short: "Attach the terminal to a run until it settles",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			status, err := attachRun(command.Context(), command.OutOrStdout(), api, args[0], remote)
			if err != nil {
				return err
			}
			if status == "failed" {
				return fmt.Errorf("run %s failed", args[0])
			}
			return nil
		},
	}

	cancel := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a run",
		Long: "Cancels a pending or running run. A promoted but not yet activated\n" +
			"deployment returns the target to the prior active revision.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			fallback, err := api.CancelRun(command.Context(), args[0])
			if err != nil {
				return err
			}
			out := command.OutOrStdout()
			fmt.Fprintf(out, "run %s cancelled\n", args[0])
			if fallback {
				fmt.Fprintln(out, "the target returned to the prior active revision")
			}
			return nil
		},
	}

	var stepKey string
	logs := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Print the logs of one step",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if stepKey == "" {
				return errors.New("--step is required")
			}
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			tree, err := api.GetRun(command.Context(), args[0])
			if err != nil {
				return err
			}
			step := findStep(tree.Steps, stepKey)
			if step == nil {
				return fmt.Errorf("run has no step %s", stepKey)
			}
			entries, _, err := api.StepLogs(command.Context(), step.ID, "", 0)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				fmt.Fprintf(command.OutOrStdout(), "%s  %-5s  %s\n",
					entry.TS.Local().Format("15:04:05"), entry.Level, entry.Message)
			}
			return nil
		},
	}
	logs.Flags().StringVar(&stepKey, "step", "", "step key, e.g. artifacts.web.build")

	command.AddCommand(show, attach, cancel, logs)
	return command
}

func newLogsCommand() *cobra.Command {
	var environment, service, remote string
	command := &cobra.Command{
		Use:   "logs [service]",
		Short: "Stream live application logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 1 {
				service = args[0]
			}
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(command.Context(), start, environment, remote)
			if err != nil {
				return err
			}
			return streamRuntimeLogs(command, target.api, target.environmentID, service)
		},
	}
	command.Flags().StringVar(&environment, "environment", "",
		"environment name (default: the checkout binding)")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	return command
}

// streamRuntimeLogs follows the runtime log stream until interrupted.
func streamRuntimeLogs(command *cobra.Command, api *client.Client, environmentID, service string) error {
	events, err := api.Stream(command.Context(), runtimeLogsPath(environmentID, service), "")
	if err != nil {
		return err
	}
	out := command.OutOrStdout()
	for event := range events {
		printLogEvent(out, event, nil)
	}
	return nil
}

// followRuntimeLogs is the compose-like attach: it tails the runtime log
// stream until ctx ends (the dev session decides what happens next: the
// pause-on-exit, or nothing after a detach). The server closes the stream
// on overflow and replays a bounded tail per fresh subscription, so the
// follow reconnects and suppresses replayed lines by their kubelet
// timestamps.
func followRuntimeLogs(ctx context.Context, out io.Writer, api *client.Client,
	environmentID, service string) error {
	lastSeen := map[string]time.Time{}
	for ctx.Err() == nil {
		events, err := api.Stream(ctx, runtimeLogsPath(environmentID, service), "")
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for event := range events {
			printLogEvent(out, event, lastSeen)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil
}

func runtimeLogsPath(environmentID, service string) string {
	path := "/v1/environments/" + environmentID + "/logs/stream"
	if service != "" {
		path += "?service=" + service
	}
	return path
}

// printLogEvent renders one runtime log SSE event. A non-nil lastSeen map
// drops lines at or before the newest timestamp already printed per pod,
// which silences the replayed tail after a reconnect.
func printLogEvent(out io.Writer, event client.SSEEvent, lastSeen map[string]time.Time) {
	if event.Event != "log" {
		return
	}
	var entry struct {
		Service  string    `json:"service"`
		Pod      string    `json:"pod"`
		Line     string    `json:"line"`
		Previous bool      `json:"previous"`
		Time     time.Time `json:"time"`
	}
	if err := json.Unmarshal([]byte(event.Data), &entry); err != nil {
		return
	}
	if lastSeen != nil && !entry.Time.IsZero() {
		if !entry.Time.After(lastSeen[entry.Pod]) {
			return
		}
		lastSeen[entry.Pod] = entry.Time
	}
	marker := ""
	if entry.Previous {
		marker = " (previous)"
	}
	fmt.Fprintf(out, "%s%s  %s\n", entry.Pod, marker, entry.Line)
}

func findStep(steps []client.Step, key string) *client.Step {
	for index := range steps {
		if steps[index].Key == key {
			return &steps[index]
		}
		if found := findStep(steps[index].Children, key); found != nil {
			return found
		}
	}
	return nil
}
