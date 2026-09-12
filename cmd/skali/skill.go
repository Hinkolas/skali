package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/skill"
)

func newSkillCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "skill",
		Short: "Manage the skali skill for coding agents",
	}
	command.AddCommand(newSkillInstallCommand())
	return command
}

func newSkillInstallCommand() *cobra.Command {
	var agentNames []string
	var all bool
	command := &cobra.Command{
		Use:   "install",
		Short: "Install the skali skill for coding agents",
		Long: "Installs the embedded skali skill (SKILL.md plus reference files on " +
			"application architecture and the skali.yaml manifest) into the user-level " +
			"skill directories of the selected coding agents. The installed directory " +
			"is owned by this command: rerun install after upgrading skali to refresh " +
			"the content.",
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

// installSkill installs the embedded skill for each agent and renders a
// per-agent summary line.
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

// tildePath shortens a home-prefixed path for display only.
func tildePath(home, path string) string {
	if home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}
