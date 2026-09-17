package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/pgtune"
	"github.com/Hinkolas/skali/internal/utils"
)

func newDatabaseCommand() *cobra.Command {
	command := &cobra.Command{
		Use:               "database",
		Short:             "Inspect and tune the managed database pools",
		ValidArgsFunction: cobra.NoFileCompletions,
		Long: "Managed PostgreSQL pools and their tuning. Every pool runs a parameter\n" +
			"set derived from its memory budget (automatic from the smallest\n" +
			"database node, or set explicitly) with per-parameter overrides on top.\n" +
			"Admin only.",
	}
	command.AddCommand(newDatabaseListCommand(), newDatabaseShowCommand(), newDatabaseSetCommand())
	return command
}

func newDatabaseListCommand() *cobra.Command {
	var remote string
	command := &cobra.Command{
		Use:               "list",
		Aliases:           []string{"ls"},
		Short:             "List the database pools with their budgets",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			pools, err := api.ListDatabasePools(ctx)
			if isReauthRequired(err) {
				if err = reauthSession(ctx, out, bufio.NewReader(command.InOrStdin()), api); err != nil {
					return err
				}
				pools, err = api.ListDatabasePools(ctx)
			}
			if err != nil {
				return err
			}
			style := clirender.StyleFor(out)
			printHeader(out, style, headerRow{"remote", api.Master(), ""})
			if len(pools) == 0 {
				fmt.Fprintln(out, style.Dim("no database pools"))
				return nil
			}
			rows := make([][]string, 0, len(pools))
			for _, pool := range pools {
				rows = append(rows, []string{
					pool.Name, pool.Class, pool.Engine + " " + strconv.Itoa(pool.Major),
					strconv.Itoa(pool.Instances), utils.FormatBytes(pool.StorageBytes),
					poolMemoryCell(pool.Memory), poolPhaseCell(pool),
				})
			}
			renderColumns(out, []string{"NAME", "CLASS", "ENGINE", "INSTANCES", "STORAGE", "MEMORY", "PHASE"}, rows)
			return nil
		},
	}
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	return command
}

func newDatabaseShowCommand() *cobra.Command {
	var remote string
	command := &cobra.Command{
		Use:               "show <name>",
		Short:             "Show a database pool's budget and effective parameters",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeDatabasePoolArg,
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			in := bufio.NewReader(command.InOrStdin())
			var pool *client.DatabasePool
			err = withReauth(ctx, out, in, api, func() (err error) {
				pool, err = api.GetDatabasePool(ctx, args[0])
				return err
			})
			var apiErr *client.APIError
			if errors.As(err, &apiErr) && apiErr.Status == 404 {
				// The list names the pools that do exist.
				if _, listErr := findDatabasePool(ctx, out, in, api, args[0]); listErr != nil {
					return listErr
				}
			}
			if err != nil {
				return err
			}
			printDatabasePool(out, api.Master(), pool)
			return nil
		},
	}
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	return command
}

func newDatabaseSetCommand() *cobra.Command {
	var (
		remote     string
		memory     string
		autoMemory bool
		sets       []string
		unsets     []string
		yes        bool
	)
	command := &cobra.Command{
		Use:   "set <name>",
		Short: "Change a database pool's memory budget or parameter overrides",
		Long: "Retunes one pool. --memory sets an explicit budget (a Kubernetes\n" +
			"quantity such as 4Gi); --auto-memory returns it to the automatic size.\n" +
			"--set key=value adds or replaces one override in PostgreSQL syntax\n" +
			"(work_mem=64MB); --unset key removes one. Parameters that reload live\n" +
			"take effect without interruption; the ones that need a restart\n" +
			"(shared_buffers, max_connections, wal_buffers, max_worker_processes)\n" +
			"restart the pool's instances, so a single-instance pool is briefly\n" +
			"unavailable. Admin only, sudo mode.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeDatabasePoolArg,
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			in := bufio.NewReader(command.InOrStdin())
			if memory != "" && autoMemory {
				return errors.New("--memory and --auto-memory are mutually exclusive")
			}
			if memory == "" && !autoMemory && len(sets) == 0 && len(unsets) == 0 {
				return errors.New("nothing to change: give --memory, --auto-memory, --set, or --unset")
			}
			input := client.DatabasePoolSettingsInput{AutoMemory: autoMemory}
			if memory != "" {
				quantity, err := resource.ParseQuantity(memory)
				if err != nil {
					return fmt.Errorf("--memory: %w (use a quantity such as 4Gi or 512Mi)", err)
				}
				bytes := quantity.Value()
				input.MemoryBytes = &bytes
			}
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			pool, err := findDatabasePool(ctx, out, in, api, args[0])
			if err != nil {
				return err
			}

			// Overrides are replaced wholesale by the API; merge the flags
			// onto the current map so unrelated keys survive.
			if len(sets) > 0 || len(unsets) > 0 {
				overrides := make(map[string]string, len(pool.Parameters.Overrides))
				for key, value := range pool.Parameters.Overrides {
					overrides[key] = value
				}
				for _, pair := range sets {
					key, value, found := strings.Cut(pair, "=")
					key, value = strings.TrimSpace(key), strings.TrimSpace(value)
					if !found || key == "" || value == "" {
						return fmt.Errorf("--set %q: expected key=value", pair)
					}
					overrides[key] = value
				}
				for _, key := range unsets {
					delete(overrides, strings.TrimSpace(key))
				}
				if err := pgtune.ValidateOverrides(overrides, 0); err != nil {
					return err
				}
				input.Parameters = overrides
			}

			style := clirender.StyleFor(out)
			printHeader(out, style,
				headerRow{"remote", api.Master(), ""},
				headerRow{"pool", pool.Name, pool.Class})
			changed := databaseSettingsChanges(pool, input)
			for _, change := range changed {
				fmt.Fprintln(out, "  "+change)
			}
			if restart := databaseRestartKeys(pool, input); len(restart) > 0 {
				fmt.Fprintln(out, style.Yellow("changing "+strings.Join(restart, ", ")+
					" restarts the pool's instances"))
			}
			if !yes {
				confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
					Title:   fmt.Sprintf("Retune pool %s?", pool.Name),
					Default: true,
				})
				if err != nil {
					return confirmError(err)
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			var updated *client.DatabasePool
			err = withReauth(ctx, out, in, api, func() (err error) {
				updated, err = api.PutDatabasePoolSettings(ctx, pool.Name, input)
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%spool %s retuned\n", style.Check(), updated.Name)
			printDatabasePoolParameters(out, style, updated)
			return nil
		},
	}
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().StringVar(&memory, "memory", "", "explicit memory budget, e.g. 4Gi")
	command.Flags().BoolVar(&autoMemory, "auto-memory", false, "size the budget automatically from the smallest database node")
	command.Flags().StringArrayVar(&sets, "set", nil, "override one parameter, key=value in PostgreSQL syntax (repeatable)")
	command.Flags().StringArrayVar(&unsets, "unset", nil, "remove one override (repeatable)")
	command.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	_ = command.RegisterFlagCompletionFunc("set", completeParameterKeys(true))
	_ = command.RegisterFlagCompletionFunc("unset", completeParameterKeys(false))
	return command
}

// completeDatabasePoolArg completes the pool name as the sole positional.
func completeDatabasePoolArg(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return noCompletions()
	}
	ctx, cancel := completionContext(command)
	defer cancel()
	api, err := queryClient(flagValue(command, "remote"))
	if err != nil {
		return noCompletions()
	}
	pools, err := api.ListDatabasePools(ctx)
	if err != nil {
		return noCompletions()
	}
	var values []cobra.Completion
	for _, pool := range pools {
		values = append(values, cobra.CompletionWithDesc(pool.Name,
			fmt.Sprintf("%s  %s %d  %s", pool.Class, pool.Engine, pool.Major, poolMemoryCell(pool.Memory))))
	}
	return filterCompletions(values, toComplete)
}

// completeParameterKeys offers the tunable parameter names; for --set the
// key is completed up to its "=" so the value stays free text.
func completeParameterKeys(withEquals bool) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if strings.Contains(toComplete, "=") {
			return noCompletions()
		}
		var values []cobra.Completion
		for _, key := range pgtune.Keys() {
			kind := pgtune.Allowed[key]
			description := "reloads live"
			if kind.Restart {
				description = "restarts the instances"
			}
			value := key
			if withEquals {
				value += "="
			}
			values = append(values, cobra.CompletionWithDesc(value, description))
		}
		matching, directive := filterCompletions(values, toComplete)
		if withEquals {
			directive |= cobra.ShellCompDirectiveNoSpace
		}
		return matching, directive
	}
}

// findDatabasePool lists the pools (the only read there is) and picks one
// by name, with the sudo replay the admin routes may ask for.
func findDatabasePool(ctx context.Context, out io.Writer, in *bufio.Reader, api *client.Client, name string) (*client.DatabasePool, error) {
	var pools []client.DatabasePool
	err := withReauth(ctx, out, in, api, func() (err error) {
		pools, err = api.ListDatabasePools(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	for i := range pools {
		if pools[i].Name == name {
			return &pools[i], nil
		}
	}
	names := make([]string, 0, len(pools))
	for _, pool := range pools {
		names = append(names, pool.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no database pool named %s (no pools exist yet)", name)
	}
	return nil, fmt.Errorf("no database pool named %s; pools: %s", name, strings.Join(names, ", "))
}

func printDatabasePool(out io.Writer, master string, pool *client.DatabasePool) {
	style := clirender.StyleFor(out)
	memory := poolMemoryCell(pool.Memory)
	note := ""
	switch {
	case pool.Memory.Bytes == nil:
		note = "waiting for a database node to report its memory"
	case pool.Memory.Auto && pool.Memory.Node != "":
		note = "automatic from node " + pool.Memory.Node
	case pool.Memory.Auto:
		note = "automatic"
	default:
		note = "explicit"
	}
	if pool.Memory.Capped {
		note += ", capped so every pool fits the node"
	}
	printHeader(out, style,
		headerRow{"remote", master, ""},
		headerRow{"pool", pool.Name, pool.Class},
		headerRow{"engine", pool.Engine + " " + strconv.Itoa(pool.Major), pool.Image},
		headerRow{"instances", strconv.Itoa(pool.Instances), poolPhaseCell(*pool)},
		headerRow{"storage", utils.FormatBytes(pool.StorageBytes), ""},
		headerRow{"memory", memory, note})
	printDatabasePoolMembers(out, pool.Members)
	printDatabasePoolDatabases(out, style, pool.Databases)
	printDatabasePoolParameters(out, style, pool)
}

// printDatabasePoolMembers lists the instance pods; the list route carries
// none, so an empty slice prints nothing.
func printDatabasePoolMembers(out io.Writer, members []client.DatabasePoolMember) {
	if len(members) == 0 {
		return
	}
	rows := make([][]string, 0, len(members))
	for _, member := range members {
		ready := "no"
		if member.Ready {
			ready = "yes"
		}
		started := "-"
		if member.StartedAt != nil {
			if at, err := time.Parse(time.RFC3339, *member.StartedAt); err == nil {
				started = poolAge(time.Since(at))
			}
		}
		role := member.Role
		if role == "" {
			role = "-"
		}
		rows = append(rows, []string{member.Name, role, member.Node, ready, strconv.Itoa(member.Restarts), started})
	}
	fmt.Fprintln(out)
	renderColumns(out, []string{"INSTANCE", "ROLE", "NODE", "READY", "RESTARTS", "STARTED"}, rows)
}

// printDatabasePoolDatabases lists every logical database on the pool with
// its owner and measured size.
func printDatabasePoolDatabases(out io.Writer, style *clirender.Style, databases []client.DatabasePoolDatabase) {
	fmt.Fprintln(out)
	if len(databases) == 0 {
		fmt.Fprintln(out, style.Dim("no databases on this pool"))
		return
	}
	rows := make([][]string, 0, len(databases))
	for _, database := range databases {
		owner, environment := "system "+database.SystemKey, "-"
		if database.Owner == "service" && database.Project != nil {
			owner = database.Project.Name + "/" + database.ServiceKey
			if database.Environment != nil {
				environment = database.Environment.Name
			}
		}
		used := "-"
		if database.UsedBytes != nil {
			used = utils.FormatBytes(*database.UsedBytes)
		}
		rows = append(rows, []string{database.DatabaseName, owner, environment,
			utils.FormatBytes(database.StorageBytes), used, database.Phase})
	}
	renderColumns(out, []string{"DATABASE", "OWNER", "ENVIRONMENT", "SIZE", "USED", "PHASE"}, rows)
}

func printDatabasePoolParameters(out io.Writer, style *clirender.Style, pool *client.DatabasePool) {
	if len(pool.Parameters.Effective) == 0 {
		fmt.Fprintln(out, style.Dim("parameters are derived once the budget is known"))
		return
	}
	keys := make([]string, 0, len(pool.Parameters.Effective))
	for key := range pool.Parameters.Effective {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([][]string, 0, len(keys))
	overridden := false
	for _, key := range keys {
		marker := ""
		if _, ok := pool.Parameters.Overrides[key]; ok {
			marker = "*"
			overridden = true
		}
		rows = append(rows, []string{key, pool.Parameters.Effective[key], marker})
	}
	fmt.Fprintln(out)
	renderColumns(out, []string{"PARAMETER", "VALUE", ""}, rows)
	if overridden {
		fmt.Fprintln(out, style.Dim("* override; the rest derive from the memory budget"))
	}
	if len(pool.Parameters.RestartKeys) > 0 {
		fmt.Fprintln(out, style.Dim("changing "+strings.Join(pool.Parameters.RestartKeys, ", ")+" restarts the instances"))
	}
}

// databaseSettingsChanges renders the before/after of one write.
func databaseSettingsChanges(pool *client.DatabasePool, input client.DatabasePoolSettingsInput) []string {
	var changes []string
	switch {
	case input.AutoMemory:
		changes = append(changes, "memory: "+poolMemoryCell(pool.Memory)+" -> automatic")
	case input.MemoryBytes != nil:
		changes = append(changes, "memory: "+poolMemoryCell(pool.Memory)+" -> "+utils.FormatBytes(*input.MemoryBytes))
	}
	if input.Parameters == nil {
		return changes
	}
	keys := map[string]bool{}
	for key := range pool.Parameters.Overrides {
		keys[key] = true
	}
	for key := range input.Parameters {
		keys[key] = true
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	for _, key := range sorted {
		before, had := pool.Parameters.Overrides[key]
		after, has := input.Parameters[key]
		switch {
		case had && has && before != after:
			changes = append(changes, key+": "+before+" -> "+after)
		case !had && has:
			derived := pool.Parameters.Effective[key]
			if derived == "" {
				derived = "derived"
			}
			changes = append(changes, key+": "+derived+" -> "+after+" (override)")
		case had && !has:
			changes = append(changes, key+": "+before+" -> derived")
		}
	}
	return changes
}

// databaseRestartKeys lists the restart-requiring keys whose effective value
// this write changes (a budget change moves shared_buffers).
func databaseRestartKeys(pool *client.DatabasePool, input client.DatabasePoolSettingsInput) []string {
	var keys []string
	if input.AutoMemory || input.MemoryBytes != nil {
		keys = append(keys, "shared_buffers")
	}
	if input.Parameters != nil {
		for _, key := range pgtune.RestartKeys() {
			before, had := pool.Parameters.Overrides[key]
			after, has := input.Parameters[key]
			if had != has || before != after {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

func poolMemoryCell(memory client.DatabasePoolMemory) string {
	if memory.Bytes == nil {
		return "unknown"
	}
	cell := utils.FormatBytes(*memory.Bytes)
	if memory.Auto {
		cell += " (auto)"
	}
	return cell
}

func poolPhaseCell(pool client.DatabasePool) string {
	if pool.Observed == nil {
		return "not observed"
	}
	return fmt.Sprintf("%s, %d/%d ready", pool.Observed.Phase, pool.Observed.ReadyInstances, pool.Observed.Instances)
}

// poolAge renders how long ago an instance started, coarsely: pods live
// days, not seconds.
func poolAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}
