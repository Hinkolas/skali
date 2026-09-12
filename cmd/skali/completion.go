package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/layout"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/skill"
	"github.com/Hinkolas/skali/internal/updates"
	"github.com/Hinkolas/skali/internal/utils"
)

// Shell completion. Cobra's generated scripts call back into `skali
// __complete`, so every value below is computed here in Go: remotes and
// manifest names offline, everything else through the API with a short
// deadline. A completer never prompts and never reports an error; a
// remote that is down simply completes nothing.

// completionTimeout bounds one remote-backed completion so a dead master
// cannot hang the tab key.
const completionTimeout = 2 * time.Second

// flagCompleters completes value-taking flags by name, wherever a command
// declares them. Registration walks the whole tree, so a new --remote or
// --environment flag is completed without touching this table.
var flagCompleters = map[string]cobra.CompletionFunc{
	"remote":         completeRemotes,
	"project":        completeProjects,
	"environment":    completeEnvironments,
	"from":           completeEnvironments,
	"promote-from":   completePromoteFrom,
	"revision":       completeRevisions,
	"step":           completeRunSteps,
	"channel":        fixedCompletions(string(updates.ChannelStable), string(updates.ChannelBeta)),
	"priority":       fixedCompletions("normal", "high"),
	"max-role":       fixedCompletions(cellRoles...),
	"deploy-policy":  fixedCompletions("direct", "promote-only"),
	"build":          fixedCompletions("local", "auto"),
	"target":         fixedCompletions("definition", "revision", "kubernetes"),
	"scope":          fixedCompletions("bundle", "node"),
	"role":           fixedCompletions(layout.RoleAgent, layout.RoleServer),
	"capabilities":   fixedCompletions(layout.Capabilities...),
	"agent":          completeAgents,
	"storage-driver": fixedCompletions("local", "longhorn"),
	"platform":       fixedCompletions("linux/amd64", "linux/arm64"),
	"manifest":       cobra.FixedCompletions([]cobra.Completion{"yml", "yaml"}, cobra.ShellCompDirectiveFilterFileExt),
	"layout":         cobra.FixedCompletions([]cobra.Completion{"yml", "yaml"}, cobra.ShellCompDirectiveFilterFileExt),
	"config":         cobra.FixedCompletions([]cobra.Completion{"yml", "yaml"}, cobra.ShellCompDirectiveFilterFileExt),
}

// registerCompletions attaches the flag completers to every command that
// declares one of the named flags, and stops file completion on commands
// that take no positional values, so `skali deploy <TAB>` offers nothing
// instead of the working directory.
func registerCompletions(root *cobra.Command) {
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		for name, completer := range flagCompleters {
			if command.LocalFlags().Lookup(name) != nil {
				_ = command.RegisterFlagCompletionFunc(name, completer)
			}
		}
		if command.ValidArgsFunction == nil && !takesPositionals(command) {
			command.ValidArgsFunction = cobra.NoFileCompletions
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
}

// takesPositionals reports whether a command's Use line declares arguments.
func takesPositionals(command *cobra.Command) bool {
	return strings.ContainsAny(command.Use, "<[")
}

func fixedCompletions(values ...string) cobra.CompletionFunc {
	completions := make([]cobra.Completion, len(values))
	for index, value := range values {
		completions[index] = cobra.Completion(value)
	}
	return cobra.FixedCompletions(completions, cobra.ShellCompDirectiveNoFileComp)
}

// completionLine is the command line cobra is completing, recorded by the
// root's PersistentPreRun when its __complete command runs. Completers
// need it to see a typed --: cobra parses the flags once with a synthetic
// -- appended, so the flag set's ArgsLenAtDash is set either way.
var completionLine []string

func recordCompletionLine(command *cobra.Command, args []string) {
	if command.Name() == cobra.ShellCompRequestCmd {
		completionLine = args
	}
}

// afterDash reports whether the word being completed follows a --.
func afterDash() bool {
	return slices.Contains(completionLine, "--")
}

// completionContext bounds a remote-backed completer.
func completionContext(command *cobra.Command) (context.Context, context.CancelFunc) {
	ctx := command.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, completionTimeout)
}

// flagValue reads a flag already parsed from the command line being
// completed, empty when the command has no such flag.
func flagValue(command *cobra.Command, name string) string {
	if flag := command.Flags().Lookup(name); flag != nil {
		return flag.Value.String()
	}
	return ""
}

// filterCompletions keeps the values the typed prefix matches.
func filterCompletions(values []cobra.Completion, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	var matching []cobra.Completion
	for _, value := range values {
		name, _, _ := strings.Cut(string(value), "\t")
		if strings.HasPrefix(name, toComplete) {
			matching = append(matching, value)
		}
	}
	return matching, cobra.ShellCompDirectiveNoFileComp
}

func noCompletions() ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// completeRemotes lists the configured remotes, offline; the dev-owned
// local remote is never a valid target.
func completeRemotes(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	cfg, err := cliconfig.Load()
	if err != nil {
		return noCompletions()
	}
	var values []cobra.Completion
	for _, name := range utils.SortedKeys(cfg.Remotes) {
		if name == localRemoteName {
			continue
		}
		values = append(values, cobra.CompletionWithDesc(name, cfg.Remotes[name].Master))
	}
	return filterCompletions(values, toComplete)
}

// completeRemoteArg completes a remote name as the sole positional.
func completeRemoteArg(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return noCompletions()
	}
	return completeRemotes(command, args, toComplete)
}

func completeProjects(command *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	ctx, cancel := completionContext(command)
	defer cancel()
	start, err := os.Getwd()
	if err != nil {
		return noCompletions()
	}
	_, _, api, err := resolveQueryRemote(start, flagValue(command, "remote"))
	if err != nil {
		return noCompletions()
	}
	projects, err := api.ListProjects(ctx)
	if err != nil {
		return noCompletions()
	}
	var values []cobra.Completion
	for _, project := range projects {
		values = append(values, cobra.CompletionWithDesc(project.Name, project.Access.Role))
	}
	return filterCompletions(values, toComplete)
}

// completionScope resolves the project the command line points at: the
// --project and --remote flags already typed, else the checkout.
func completionScope(command *cobra.Command, ctx context.Context) (*queryProject, error) {
	start, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return resolveQueryProject(ctx, start, flagValue(command, "project"), flagValue(command, "environment"), flagValue(command, "remote"))
}

func environmentCompletions(scope *queryProject) []cobra.Completion {
	var values []cobra.Completion
	for _, environment := range scope.environments {
		values = append(values, cobra.CompletionWithDesc(environment.Name, environment.Access))
	}
	return values
}

func completeEnvironments(command *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	ctx, cancel := completionContext(command)
	defer cancel()
	scope, err := completionScope(command, ctx)
	if err != nil {
		return noCompletions()
	}
	return filterCompletions(environmentCompletions(scope), toComplete)
}

// completeEnvironmentArg completes an environment name as the sole
// positional (env remove).
func completeEnvironmentArg(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return noCompletions()
	}
	return completeEnvironments(command, args, toComplete)
}

// completePromoteFrom completes the last item of the comma list, keeping
// the items already typed; `any` is the literal that lifts the restriction.
func completePromoteFrom(command *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	ctx, cancel := completionContext(command)
	defer cancel()
	prefix, last := "", toComplete
	if index := strings.LastIndex(toComplete, ","); index >= 0 {
		prefix, last = toComplete[:index+1], toComplete[index+1:]
	}
	values := []cobra.Completion{cobra.CompletionWithDesc("any", "promote from every environment")}
	if scope, err := completionScope(command, ctx); err == nil {
		for _, environment := range scope.environments {
			if strings.Contains(prefix, environment.Name+",") {
				continue
			}
			values = append(values, cobra.CompletionWithDesc(environment.Name, environment.Access))
		}
	}
	if prefix != "" {
		values = slices.DeleteFunc(values, func(value cobra.Completion) bool { return strings.HasPrefix(string(value), "any\t") })
		for index, value := range values {
			values[index] = cobra.Completion(prefix) + value
		}
		last = toComplete
	}
	return filterCompletions(values, last)
}

// manifestApplications reads the checkout's manifest without compiling
// it, so a half-edited manifest still completes what it declares.
func manifestApplications() map[string]manifest.Application {
	start, err := os.Getwd()
	if err != nil {
		return nil
	}
	path, err := manifest.Discover("", start)
	if err != nil {
		return nil
	}
	document, err := manifest.ParseFile(path)
	if err != nil {
		return nil
	}
	return document.Project.Applications
}

// completeServiceArg completes the manifest's application names as the
// first positional; after -- the shell completes the command to run.
func completeServiceArg(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if afterDash() {
		return nil, cobra.ShellCompDirectiveDefault
	}
	if len(args) > 0 {
		return noCompletions()
	}
	var values []cobra.Completion
	for _, key := range utils.SortedKeys(manifestApplications()) {
		values = append(values, cobra.Completion(key))
	}
	return filterCompletions(values, toComplete)
}

// completeDevRunArgs follows the dev run grammar: first an application or a
// command name only one application declares, then that application's
// commands; after -- the shell completes the raw command.
func completeDevRunArgs(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if afterDash() {
		return nil, cobra.ShellCompDirectiveDefault
	}
	applications := manifestApplications()
	var values []cobra.Completion
	switch len(args) {
	case 0:
		owners := map[string]int{}
		for _, application := range applications {
			for name := range application.Commands {
				owners[name]++
			}
		}
		for _, key := range utils.SortedKeys(applications) {
			values = append(values, cobra.CompletionWithDesc(key, "application"))
		}
		for _, name := range utils.SortedKeys(owners) {
			if owners[name] != 1 {
				continue
			}
			for key, application := range applications {
				if argv, ok := application.Commands[name]; ok {
					values = append(values, cobra.CompletionWithDesc(name, key+": "+shellWords(argv)))
				}
			}
		}
	case 1:
		application, ok := applications[args[0]]
		if !ok {
			return noCompletions()
		}
		for _, name := range utils.SortedKeys(application.Commands) {
			values = append(values, cobra.CompletionWithDesc(name, shellWords(application.Commands[name])))
		}
	default:
		return noCompletions()
	}
	return filterCompletions(values, toComplete)
}

// completionTarget resolves the environment the command line points at.
func completionTarget(command *cobra.Command, ctx context.Context) (*queryTarget, error) {
	start, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return resolveQueryTarget(ctx, start, flagValue(command, "environment"), flagValue(command, "remote"))
}

// completeRunIDArg completes the environment's run ids, newest first as
// the server lists them.
func completeRunIDArg(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return noCompletions()
	}
	ctx, cancel := completionContext(command)
	defer cancel()
	target, err := completionTarget(command, ctx)
	if err != nil {
		return noCompletions()
	}
	runs, err := target.api.ListRuns(ctx, target.environmentID)
	if err != nil {
		return noCompletions()
	}
	var values []cobra.Completion
	for _, run := range runs {
		values = append(values, cobra.CompletionWithDesc(run.ID, run.Kind+" "+run.Status))
	}
	return filterCompletions(values, toComplete)
}

// completeRunSteps completes --step with the keys of the run named on the
// line.
func completeRunSteps(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return noCompletions()
	}
	ctx, cancel := completionContext(command)
	defer cancel()
	api, err := queryClient(flagValue(command, "remote"))
	if err != nil {
		return noCompletions()
	}
	tree, err := api.GetRun(ctx, args[0])
	if err != nil {
		return noCompletions()
	}
	var values []cobra.Completion
	var walk func(steps []client.Step)
	walk = func(steps []client.Step) {
		for index := range steps {
			if steps[index].Key != "" {
				values = append(values, cobra.CompletionWithDesc(steps[index].Key, steps[index].Title))
			}
			walk(steps[index].Children)
		}
	}
	walk(tree.Steps)
	return filterCompletions(values, toComplete)
}

// completeSnapshotArg completes the project's snapshot ids for restore.
func completeSnapshotArg(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return noCompletions()
	}
	ctx, cancel := completionContext(command)
	defer cancel()
	scope, err := completionScope(command, ctx)
	if err != nil {
		return noCompletions()
	}
	snapshots, err := scope.api.ListProjectBackups(ctx, scope.project.ID)
	if err != nil {
		return noCompletions()
	}
	var values []cobra.Completion
	for index := range snapshots {
		snapshot := &snapshots[index]
		values = append(values, cobra.CompletionWithDesc(snapshot.ID,
			fmt.Sprintf("%s  %s  %s", snapshotTime(snapshot), snapshot.Environment, utils.FormatBytes(snapshot.Bytes))))
	}
	return filterCompletions(values, toComplete)
}

// completeRevisions completes --revision with the environment's stored
// revision ids.
func completeRevisions(command *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	ctx, cancel := completionContext(command)
	defer cancel()
	target, err := completionTarget(command, ctx)
	if err != nil {
		return noCompletions()
	}
	revisions, err := target.api.ListRevisions(ctx, target.environmentID)
	if err != nil {
		return noCompletions()
	}
	var values []cobra.Completion
	for _, revision := range revisions {
		values = append(values, cobra.CompletionWithDesc(revision.ID,
			utils.ShortChecksum(revision.Checksum)+"  "+revision.CreatedAt.Local().Format("2006-01-02 15:04")))
	}
	return filterCompletions(values, toComplete)
}

// completeMembers completes the project's members by email.
func completeMembers(command *cobra.Command, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	ctx, cancel := completionContext(command)
	defer cancel()
	scope, err := completionScope(command, ctx)
	if err != nil {
		return noCompletions()
	}
	members, err := scope.api.ListMembers(ctx, scope.project.ID)
	if err != nil {
		return noCompletions()
	}
	var values []cobra.Completion
	for _, member := range members {
		values = append(values, cobra.CompletionWithDesc(member.Email, member.Role))
	}
	return filterCompletions(values, toComplete)
}

// completeMemberArg completes a member as the sole positional.
func completeMemberArg(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return noCompletions()
	}
	return completeMembers(command, toComplete)
}

// completeAccessSetArgs completes access set: the member, then the role on
// the ladder the level uses (none only exists per environment).
func completeAccessSetArgs(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeMembers(command, toComplete)
	case 1:
		roles := projectRoles
		if flagValue(command, "environment") != "" {
			roles = cellRoles
		}
		return fixedCompletions(roles...)(command, args, toComplete)
	}
	return noCompletions()
}

// completeNodeCapabilities completes cluster node capabilities: the node
// name is the operator's to type, every further word is a capability.
func completeNodeCapabilities(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return noCompletions()
	}
	remaining := slices.DeleteFunc(slices.Clone(layout.Capabilities), func(capability string) bool {
		return slices.Contains(args[1:], capability)
	})
	return fixedCompletions(remaining...)(command, args, toComplete)
}

func completeAgents(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	var values []cobra.Completion
	for _, agent := range skill.Agents() {
		values = append(values, cobra.CompletionWithDesc(agent.Name, agent.Label))
	}
	return filterCompletions(values, toComplete)
}

// --- the completion command group ---

// completionShells are the shells cobra generates scripts for; install
// knows where the first three load them from.
var completionShells = []string{"bash", "zsh", "fish", "powershell"}

func newCompletionCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "completion",
		Short: "Print or install shell completions",
		Long: "Shell completions complete skali's commands and flags and, where a\n" +
			"value has a known set, the values too: remotes, environments, projects,\n" +
			"run and snapshot ids, members, the manifest's applications and named\n" +
			"commands, and fixed choices such as roles and channels. Remote-backed\n" +
			"values come from the remote the command line points at and give up\n" +
			"silently after two seconds.\n\n" +
			"skali completion install writes the script where your shell loads it;\n" +
			"install.sh runs it for your login shell. The shell subcommands print\n" +
			"the script for a manual setup.",
	}
	for _, shell := range completionShells {
		command.AddCommand(newCompletionShellCommand(shell))
	}
	command.AddCommand(newCompletionInstallCommand())
	return command
}

func newCompletionShellCommand(shell string) *cobra.Command {
	return &cobra.Command{
		Use:   shell,
		Short: "Print the " + shell + " completion script",
		Long: "Prints the " + shell + " completion script to stdout. Source it from your\n" +
			"shell's startup file, or let skali completion install place it where\n" +
			"the shell loads it on its own.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return completionScript(command.Root(), shell, command.OutOrStdout())
		},
	}
}

// completionScript writes the generated script for one shell.
func completionScript(root *cobra.Command, shell string, out io.Writer) error {
	switch shell {
	case "bash":
		return root.GenBashCompletionV2(out, true)
	case "zsh":
		return root.GenZshCompletion(out)
	case "fish":
		return root.GenFishCompletion(out, true)
	case "powershell":
		return root.GenPowerShellCompletionWithDesc(out)
	}
	return fmt.Errorf("unsupported shell %q; expected one of %s", shell, strings.Join(completionShells, ", "))
}
