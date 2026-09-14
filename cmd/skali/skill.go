package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/skill"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func newSkillCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "skill",
		Short: "Manage the skali skill for coding agents",
		Long: "The skill has two parts (docs/versioning.md, decision 6). skill install " +
			"writes a small operational guide into the " +
			"agent's skill directory once; skali upgrade refreshes it. skill read " +
			"prints the version-bound references (the manifest grammar, the CLI " +
			"surface and platform architecture) and dispatches like every workflow command, so run from a " +
			"project directory it answers at the release of the project's target " +
			"cluster.",
	}
	command.AddCommand(newSkillInstallCommand(), newSkillReadCommand())
	return command
}

func newSkillInstallCommand() *cobra.Command {
	var agentNames []string
	var all bool
	command := &cobra.Command{
		Use:   "install",
		Short: "Install the skali skill for coding agents",
		Long: "Installs the skali operational skill shell (SKILL.md) " +
			"into the user-level skill directories of the selected coding " +
			"agents. The shell tells the agent to read the manifest, architecture and CLI references " +
			"through skali skill read, which answers at the release of the project's " +
			"target cluster. The installed directory is owned by this command and is " +
			"refreshed by skali upgrade.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if len(agentNames) == 0 && !all && !cliprompt.Interactive() {
				return errors.New("non-interactive runs must select agents: pass --agent claude, --agent codex, or --all")
			}
			out := os.Stdout
			agents, err := selectAgents(command.Context(), promptSession(out, bufio.NewReader(os.Stdin)), agentNames, all)
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			return installSkill(out, home, agents)
		},
	}
	command.Flags().StringSliceVar(&agentNames, "agent", nil, "agent to install for: claude or codex; repeatable")
	command.Flags().BoolVar(&all, "all", false, "install for every supported agent")
	command.MarkFlagsMutuallyExclusive("agent", "all")
	return command
}

// newSkillReadCommand serves the version-bound references. Under dispatch
// the binary that prints is the one for the project's target cluster, so
// the reference is right by construction; --since renders the ledger for
// a manifest whose watermark is older than that release.
func newSkillReadCommand() *cobra.Command {
	var since string
	command := &cobra.Command{
		Use:   "read [topic]",
		Short: "Print a skali reference at the target's release",
		Long: "Prints one of the references coding agents read instead of guessing: " +
			"manifest (grammar), architecture (platform behavior), or " +
			"cli (the commands used from the terminal). Without a topic the topics are " +
			"listed. Run from the project directory the command dispatches to the " +
			"release of the project's target cluster, so the reference matches the " +
			"cluster that will compile the manifest. --since <release> prints the " +
			"manifest grammar changes since that release instead of the reference " +
			"(the same ledger skali validate and skali manifest upgrade consult).",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeTopics,
		RunE: func(command *cobra.Command, args []string) error {
			out := command.OutOrStdout()
			fmt.Fprintln(out, versionDescription())
			fmt.Fprintln(out)
			if len(args) == 0 {
				if since != "" {
					return errors.New("--since applies to the manifest topic; run skali skill read manifest --since <release>")
				}
				listTopics(out)
				return nil
			}
			topic := args[0]
			content, ok := skill.Reference(topic)
			if !ok {
				return fmt.Errorf("unknown topic %q (valid: %s)", topic, strings.Join(skill.TopicNames(), ", "))
			}
			if since == "" {
				_, err := out.Write(content)
				return err
			}
			if topic != "manifest" {
				return errors.New("--since applies to the manifest topic; run skali skill read manifest --since <release>")
			}
			release, ok := manifest.Watermark(since)
			if !ok {
				return fmt.Errorf("--since %q is not a skali release; expected a tag like %s", since, manifest.ReferenceRelease())
			}
			renderChangesSince(out, release, versionpkg.Version, manifest.ChangesSince(release))
			return nil
		},
	}
	addVersionFlags(command, true)
	command.Flags().StringVar(&since, "since", "", "print the manifest changes since this release instead of the reference")
	return command
}

// listTopics prints the topics of the binary that answers, so under
// dispatch the release named is the target cluster's.
func listTopics(out io.Writer) {
	fmt.Fprintf(out, "references of skali %s\n", versionpkg.Version)
	for _, topic := range skill.Topics() {
		fmt.Fprintf(out, "  %-9s %s\n", topic.Name, topic.Summary)
	}
	fmt.Fprintln(out, "run skali skill read <topic> from the project directory")
}

// renderChangesSince prints the ledger entries after a watermark, oldest
// first, each with its message and migration hint.
func renderChangesSince(out io.Writer, since, current string, changes []manifest.Change) {
	if len(changes) == 0 {
		fmt.Fprintf(out, "no manifest changes since %s; this skali is %s\n", since, current)
		return
	}
	fmt.Fprintf(out, "manifest changes since %s (this skali is %s)\n", since, current)
	for _, change := range changes {
		fmt.Fprintf(out, "  %s  %s  %s\n", change.Release, change.Kind, change.Path)
		fmt.Fprintf(out, "    %s\n", change.Message)
		if change.Hint != "" {
			fmt.Fprintf(out, "    fix: %s\n", change.Hint)
		}
	}
}

func completeTopics(_ *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var values []cobra.Completion
	for _, topic := range skill.Topics() {
		values = append(values, cobra.CompletionWithDesc(topic.Name, topic.Summary))
	}
	return filterCompletions(values, toComplete)
}

// selectAgents resolves the requested agents from flags, or asks with every
// agent preselected when nothing was requested.
func selectAgents(ctx context.Context, session *cliprompt.Session, names []string, all bool) ([]skill.Agent, error) {
	if all {
		return skill.Agents(), nil
	}
	if len(names) > 0 {
		agents := make([]skill.Agent, 0, len(names))
		seen := make(map[string]bool, len(names))
		for _, name := range names {
			if seen[name] {
				continue
			}
			agent, err := skill.AgentByName(name)
			if err != nil {
				return nil, err
			}
			seen[name] = true
			agents = append(agents, agent)
		}
		return agents, nil
	}

	supported := skill.Agents()
	options := make([]cliprompt.Option, 0, len(supported))
	defaults := make([]string, 0, len(supported))
	for _, agent := range supported {
		options = append(options, cliprompt.Option{
			Label:       agent.Label,
			Description: agent.Dir("~"),
			Value:       agent.Name,
		})
		defaults = append(defaults, agent.Name)
	}
	selected, err := session.MultiSelect(ctx, cliprompt.MultiSelectOptions{
		Title:         "Install the skali skill for which agents?",
		Description:   "The skill teaches coding agents skali.yaml and skali app architecture.",
		Options:       options,
		DefaultValues: defaults,
		Validate: func(values []string) error {
			if len(values) == 0 {
				return errors.New("select at least one agent")
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	agents := make([]skill.Agent, 0, len(selected))
	for _, name := range selected {
		agent, err := skill.AgentByName(name)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

// installSkill installs the shell for each agent and renders a per-agent
// summary line.
func installSkill(out io.Writer, home string, agents []skill.Agent) error {
	style := clirender.StyleFor(out)
	for _, agent := range agents {
		paths, err := skill.Install(home, agent)
		if err != nil {
			return fmt.Errorf("install %s skill: %w", agent.Label, err)
		}
		fmt.Fprintf(out, "%sinstalled %s skill to %s (%d files)\n",
			style.Check(), agent.Label, tildePath(home, agent.Dir(home)), len(paths))
	}
	fmt.Fprintln(out, "Restart agent sessions to pick up the new skill.")
	return nil
}

// refreshSkill reinstalls the skill for every agent that already has a
// managed copy under home, by running the freshly installed binary so the
// content written is its own. Nothing is installed anew, and a failure is
// a warning for the caller, never a failed upgrade.
func refreshSkill(ctx context.Context, tasks *clirender.Tasks, executable, home string) string {
	if home == "" {
		return ""
	}
	args := []string{"skill", "install"}
	var labels []string
	for _, agent := range skill.Agents() {
		if skill.Installed(home, agent) {
			args = append(args, "--agent", agent.Name)
			labels = append(labels, agent.Label)
		}
	}
	if len(labels) == 0 {
		return ""
	}
	task := tasks.Start("Refresh agent skill")
	err := retryTextFileBusy(ctx, func() error {
		output, err := exec.CommandContext(ctx, executable, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	})
	if err != nil {
		task.Fail()
		return fmt.Sprintf("refresh agent skill: %v; run skali skill install", err)
	}
	task.Done(strings.Join(labels, ", "))
	return ""
}

// tildePath shortens a home-prefixed path for display only.
func tildePath(home, path string) string {
	if home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}
