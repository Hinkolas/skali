package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
)

// environmentIDByName resolves --environment against the current context.
func environmentIDByName(command *cobra.Command, api *client.Client, environment string) (string, error) {
	ctx := command.Context()
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	for _, project := range projects {
		environments, err := api.ListEnvironments(ctx, project.ID)
		if err != nil {
			return "", err
		}
		for _, candidate := range environments {
			if candidate.Name == environment {
				return candidate.ID, nil
			}
		}
	}
	return "", fmt.Errorf("environment %s not found on this installation", environment)
}

func newRunsCommand() *cobra.Command {
	var environment string
	command := &cobra.Command{
		Use:   "runs",
		Short: "List an environment's runs",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if environment == "" {
				return errors.New("--environment is required")
			}
			_, _, api, err := currentClient()
			if err != nil {
				return err
			}
			environmentID, err := environmentIDByName(command, api, environment)
			if err != nil {
				return err
			}
			runs, err := api.ListRuns(command.Context(), environmentID)
			if err != nil {
				return err
			}
			out := command.OutOrStdout()
			fmt.Fprintf(out, "%-36s  %-11s  %-9s  %s\n", "RUN", "KIND", "STATUS", "STARTED")
			for _, run := range runs {
				started := ""
				if run.StartedAt != nil {
					started = humanSince(*run.StartedAt)
				}
				fmt.Fprintf(out, "%-36s  %-11s  %-9s  %s\n", run.ID, run.Kind, run.Status, started)
			}
			return nil
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment name")
	return command
}

func newRunCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "run",
		Short: "Inspect, attach to, or cancel a run",
	}

	show := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Print a run's step tree",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			_, _, api, err := currentClient()
			if err != nil {
				return err
			}
			tree, err := api.GetRun(command.Context(), args[0])
			if err != nil {
				return err
			}
			for _, line := range clirender.Lines(tree, nil) {
				fmt.Fprintln(command.OutOrStdout(), line)
			}
			return nil
		},
	}

	attach := &cobra.Command{
		Use:   "attach <run-id>",
		Short: "Attach the terminal to a run until it settles",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			_, _, api, err := currentClient()
			if err != nil {
				return err
			}
			status, err := attachRun(command.Context(), command.OutOrStdout(), api, args[0])
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
			_, _, api, err := currentClient()
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
			_, _, api, err := currentClient()
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
	var environment, service string
	command := &cobra.Command{
		Use:   "logs [service]",
		Short: "Stream live application logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if environment == "" {
				return errors.New("--environment is required")
			}
			if len(args) == 1 {
				service = args[0]
			}
			_, _, api, err := currentClient()
			if err != nil {
				return err
			}
			environmentID, err := environmentIDByName(command, api, environment)
			if err != nil {
				return err
			}
			return streamRuntimeLogs(command, api, environmentID, service)
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment name")
	return command
}

// streamRuntimeLogs follows the runtime log stream until interrupted.
func streamRuntimeLogs(command *cobra.Command, api *client.Client, environmentID, service string) error {
	path := "/v1/environments/" + environmentID + "/logs/stream"
	if service != "" {
		path += "?service=" + service
	}
	events, err := api.Stream(command.Context(), path, "")
	if err != nil {
		return err
	}
	out := command.OutOrStdout()
	for event := range events {
		if event.Event != "log" {
			continue
		}
		var entry struct {
			Service  string `json:"service"`
			Pod      string `json:"pod"`
			Line     string `json:"line"`
			Previous bool   `json:"previous"`
		}
		if err := json.Unmarshal([]byte(event.Data), &entry); err != nil {
			continue
		}
		marker := ""
		if entry.Previous {
			marker = " (previous)"
		}
		fmt.Fprintf(out, "%s%s  %s\n", entry.Pod, marker, entry.Line)
	}
	return nil
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

func humanSince(t time.Time) string {
	elapsed := time.Since(t).Round(time.Second)
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}
