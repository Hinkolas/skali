package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// Seams for the unit tests: prune must reason about records without a
// docker daemon or k3d in the loop.
var (
	localStatuses      = localdev.Statuses
	deleteLocalCluster = localdev.DeleteFor
)

func newDevPruneCommand() *cobra.Command {
	var yes bool
	command := &cobra.Command{
		Use:   "prune",
		Short: "Delete the local platforms no remote and no skali in use runs",
		Long: "Each skali release has its own local platform (a k3d cluster with\n" +
			"its own data). A platform whose release no remote cluster on this\n" +
			"machine runs and that is not this skali's release is no longer\n" +
			"reachable by any command, and prune deletes it with its data after\n" +
			"confirmation. The platform of a working tree stays while this skali\n" +
			"is a development build; the shared platform of releases before\n" +
			"v0.1.0-rc.3 is always offered. Nothing outside this machine is\n" +
			"affected. This command always runs in the installed skali.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runDevPrune(command, yes)
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "skip the confirmation")
	return command
}

func runDevPrune(command *cobra.Command, yes bool) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	style := clirender.StyleFor(out)
	cfg, err := cliconfig.Load()
	if err != nil {
		return err
	}
	records, err := localdev.Records()
	if err != nil {
		return err
	}
	statuses, err := localStatuses(ctx)
	if err != nil {
		return err
	}
	home := versionpkg.Version
	keep := referencedReleases(cfg, home, false)
	candidates := pruneCandidates(records, keep, home)
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing to prune: every local platform is in use")
		for _, record := range records {
			fmt.Fprintln(out, "  "+platformLine(record, statuses, cfg, home))
		}
		return nil
	}

	fmt.Fprintln(out, style.BoldRed("These local platforms are not in use and will be deleted with all their data:"))
	for _, record := range candidates {
		fmt.Fprintln(out, "  "+platformLine(record, statuses, cfg, home))
	}
	fmt.Fprintln(out, "Nothing outside this machine is affected.")
	if !yes {
		confirmed, err := cliprompt.New(os.Stdin, out).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: "Delete these local platforms and their data?",
		})
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("aborted")
		}
	}
	pruned := map[string]bool{}
	for _, record := range candidates {
		if err := deletePlatform(ctx, out, record, statuses); err != nil {
			return err
		}
		if record.Version != "" {
			pruned[record.Version] = true
		}
	}
	// The local remote's token belongs to whichever platform last logged
	// in; when that platform is gone the record only misleads.
	if local := cfg.Remotes[localRemoteName]; local != nil && pruned[local.Version] {
		delete(cfg.Remotes, localRemoteName)
		if err := cliconfig.Save(cfg); err != nil {
			return err
		}
	}
	return nil
}

// pruneCandidates picks the records nothing uses: a release no remote and
// not home names, the working tree under a released home (it has none),
// and the legacy shared platform always.
func pruneCandidates(records []localdev.Record, keep map[string]bool, home string) []localdev.Record {
	var candidates []localdev.Record
	for _, record := range records {
		switch {
		case record.Legacy:
			candidates = append(candidates, record)
		case record.Version == "":
			if versionpkg.IsRelease(home) {
				candidates = append(candidates, record)
			}
		case !keep[record.Version]:
			candidates = append(candidates, record)
		}
	}
	return candidates
}

// platformLine renders one recorded platform: name, what it runs, its
// docker state, and who uses it.
func platformLine(record localdev.Record, statuses map[string]localdev.ClusterStatus, cfg *cliconfig.Config, home string) string {
	var users []string
	switch {
	case record.Legacy:
		users = append(users, "created before per-release platforms")
	case record.Version == "":
		if !versionpkg.IsRelease(home) {
			users = append(users, "this skali")
		}
	default:
		if record.Version == home {
			users = append(users, "this skali")
		}
		var remotes []string
		for name, remote := range cfg.Remotes {
			if name != localRemoteName && remote.Version == record.Version {
				remotes = append(remotes, name)
			}
		}
		slices.Sort(remotes)
		for _, name := range remotes {
			users = append(users, "remote "+name)
		}
	}
	line := fmt.Sprintf("%-32s %-30s %-8s", record.Name, platformLabel(record), localdev.StatusOf(statuses, record.Name))
	if len(users) > 0 {
		line += " " + strings.Join(users, ", ")
	}
	return strings.TrimRight(line, " ")
}

// deletePlatform removes one recorded platform: the cluster when it exists
// (a record without one is just stale), then the record.
func deletePlatform(ctx context.Context, out io.Writer, record localdev.Record, statuses map[string]localdev.ClusterStatus) error {
	tasks := clirender.NewTasks(out)
	if localdev.StatusOf(statuses, record.Name) != localdev.ClusterAbsent {
		task := tasks.Start("Delete cluster " + record.Name + " and volumes")
		if err := deleteLocalCluster(ctx, record.Name); err != nil {
			task.Fail()
			return err
		}
		task.Done("")
	}
	task := tasks.Start("Remove record " + record.Name)
	var err error
	if record.Legacy {
		err = localdev.RemoveLegacyRecord()
	} else {
		err = localdev.RemoveRecord(record.Name)
	}
	if err != nil {
		task.Fail()
		return err
	}
	task.Done("")
	return nil
}
