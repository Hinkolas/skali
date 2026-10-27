package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/build"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/utils"
)

// insightsSlowestSteps is how many of a build's slowest steps the summary
// lists.
const insightsSlowestSteps = 5

func newRunInsightsCommand(remote *string) *cobra.Command {
	var stepKey string
	var withLog bool
	command := &cobra.Command{
		Use:   "insights <run-id>",
		Short: "Print the insights of a run's builds",
		Long: "Prints what each local build of the run reported once it ended: its\n" +
			"outcome and duration, how many of its steps were cached, and its\n" +
			"slowest steps. --log adds the builder's full output. A build that\n" +
			"was interrupted, or ran with a CLI or server from before build\n" +
			"insights, has none.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeRunIDArg,
		RunE: func(command *cobra.Command, args []string) error {
			api, err := queryClient(*remote)
			if err != nil {
				return err
			}
			tree, err := api.GetRun(command.Context(), args[0])
			if err != nil {
				return err
			}
			var steps []client.Step
			if stepKey != "" {
				step := findStep(tree.Steps, stepKey)
				if step == nil {
					return fmt.Errorf("run has no step %s", stepKey)
				}
				steps = []client.Step{*step}
			} else {
				steps = buildSteps(tree.Steps)
				if len(steps) == 0 {
					return fmt.Errorf("run %s built nothing", tree.Run.ID)
				}
			}
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			for index, step := range steps {
				if index > 0 {
					fmt.Fprintln(out)
				}
				fmt.Fprintf(out, "%s  %s\n", style.Bold(buildStepApplication(step.Key)), step.Title)
				report, err := api.BuildInsights(command.Context(), step.ID)
				if client.IsNotFound(err) {
					fmt.Fprintln(out, style.Dim("No insights available for this build."))
					continue
				}
				if err != nil {
					return err
				}
				printBuildInsights(out, style, report, withLog)
			}
			return nil
		},
	}
	command.Flags().StringVar(&stepKey, "step", "", "one build step's key, e.g. artifacts.web.build (default: every build of the run)")
	command.Flags().BoolVar(&withLog, "log", false, "also print the builder's full output")
	return command
}

// buildSteps collects a run's build steps, artifacts.<application>.build,
// in tree order.
func buildSteps(steps []client.Step) []client.Step {
	var found []client.Step
	for _, step := range steps {
		if buildStepApplication(step.Key) != "" {
			found = append(found, step)
		}
		found = append(found, buildSteps(step.Children)...)
	}
	return found
}

// buildStepApplication names the application a build step builds, empty
// for any other step.
func buildStepApplication(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "artifacts" || parts[2] != "build" {
		return ""
	}
	return parts[1]
}

func printBuildInsights(out io.Writer, style *clirender.Style, report *build.Insights, withLog bool) {
	outcome := style.Green(report.Outcome)
	if report.Outcome != build.InsightsSucceeded {
		outcome = style.Red(report.Outcome)
	}
	duration := report.FinishedAt.Sub(report.StartedAt).Round(100 * time.Millisecond)
	cached := 0
	for _, step := range report.Steps {
		if step.Status == build.StepCached {
			cached++
		}
	}
	rows := []headerRow{
		{"outcome", outcome, ""},
		{"duration", duration.String(), report.StartedAt.Local().Format("2006-01-02 15:04:05")},
		{"platform", report.Platform, report.Engine},
		{"steps", utils.PluralCount(len(report.Steps), "step"), fmt.Sprintf("%d cached", cached)},
	}
	lines := utils.PluralCount(report.LogLines, "line")
	if report.LogTruncated {
		rows = append(rows, headerRow{"log", lines, "kept its start and end"})
	} else {
		rows = append(rows, headerRow{"log", lines, ""})
	}
	if report.Error != "" {
		rows = append(rows, headerRow{"error", style.Red(report.Error), ""})
	}
	printHeader(out, style, rows...)

	slowest := slices.Clone(report.Steps)
	slowest = slices.DeleteFunc(slowest, func(step build.InsightsStep) bool { return step.Seconds <= 0 })
	slices.SortStableFunc(slowest, func(a, b build.InsightsStep) int {
		switch {
		case a.Seconds > b.Seconds:
			return -1
		case a.Seconds < b.Seconds:
			return 1
		}
		return 0
	})
	if len(slowest) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, style.Dim("slowest steps"))
		for _, step := range slowest[:min(len(slowest), insightsSlowestSteps)] {
			fmt.Fprintf(out, "  %7.1fs  %s\n", step.Seconds, step.Name)
		}
	}
	if withLog && report.Log != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, report.Log)
	}
}
