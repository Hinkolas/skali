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

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/utils"
)

type rollbackOptions struct {
	Remote      string
	Environment string
	Revision    string
	Yes         bool
	Detach      bool
}

func newRollbackCommand() *cobra.Command {
	opts := &rollbackOptions{}
	command := &cobra.Command{
		Use:   "rollback",
		Short: "Roll the environment back to a previous revision",
		Long: "Re-points the environment target at a stored revision and rolls it\n" +
			"out exactly as stored: same definition, images, and values. Nothing\n" +
			"is rebuilt and no new revision is created.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runRollback(command, opts)
		},
	}
	command.Flags().StringVar(&opts.Remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().StringVar(&opts.Environment, "environment", "",
		"environment name (default: the checkout binding)")
	command.Flags().StringVar(&opts.Revision, "revision", "",
		"revision to roll back to (a revision id or unique checksum prefix)")
	command.Flags().BoolVar(&opts.Yes, "yes", false, "skip the confirmation prompt")
	command.Flags().BoolVar(&opts.Detach, "detach", false,
		"start the rollback and return without attaching to the run")
	return command
}

func runRollback(command *cobra.Command, opts *rollbackOptions) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	style := clirender.StyleFor(out)

	start, err := os.Getwd()
	if err != nil {
		return err
	}
	target, err := resolveQueryTarget(ctx, start, opts.Environment, opts.Remote)
	if err != nil {
		return err
	}
	pointer, err := target.api.Target(ctx, target.environmentID)
	if err != nil {
		return err
	}
	revisions, err := target.api.ListRevisions(ctx, target.environmentID)
	if err != nil {
		return err
	}
	if len(revisions) == 0 {
		return fmt.Errorf("environment %s has no revisions", target.environment)
	}

	fmt.Fprintf(out, "%s  %s %s\n", style.Dim("environment"),
		target.environment, style.Dim("("+target.remoteName+")"))

	var chosen *client.RevisionSummary
	if opts.Revision != "" {
		if chosen, err = resolveRevisionArg(revisions, opts.Revision); err != nil {
			return err
		}
	} else {
		if !cliprompt.Interactive() || opts.Yes {
			return errors.New("--revision is required (a revision id or unique checksum prefix)")
		}
		if chosen, err = chooseRevision(out, bufio.NewReader(os.Stdin), revisions, pointer); err != nil {
			return err
		}
	}
	if pointer.TargetRevisionID != nil && *pointer.TargetRevisionID == chosen.ID {
		return fmt.Errorf("the environment already targets revision %s", utils.ShortChecksum(chosen.Checksum))
	}

	if !opts.Yes {
		if !cliprompt.Interactive() {
			return errors.New("non-interactive use requires --yes")
		}
		confirmed, err := cliprompt.New(os.Stdin, out).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: fmt.Sprintf("Roll back %s to revision %s (%s)?", target.environment,
				utils.ShortChecksum(chosen.Checksum), utils.HumanSince(chosen.CreatedAt)),
		})
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("aborted")
		}
	}

	result, err := target.api.SetTarget(ctx, target.environmentID, chosen.ID)
	if err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		fmt.Fprintln(out, "warning: "+warning.Message)
	}
	fmt.Fprintf(out, "\n%s %s  roll back %s to %s\n", style.Dim("run"),
		style.Bold(result.RunID), target.environment, utils.ShortChecksum(chosen.Checksum))
	if opts.Detach {
		fmt.Fprintf(out, "rollback continues on the server; attach with: %s\n", runAttachHint(opts.Remote, result.RunID))
		return nil
	}
	status, err := attachRun(ctx, out, target.api, result.RunID, opts.Remote)
	if err != nil {
		return err
	}
	switch status {
	case "succeeded":
		fmt.Fprintln(out, "\n"+style.Check()+style.Bold(style.Green("ready")))
		printReadySummary(ctx, out, target.api, target.environmentID, remoteReadySummary(target.remoteName))
		return nil
	case "failed":
		return fmt.Errorf("run %s failed", result.RunID)
	case "cancelled":
		return fmt.Errorf("run %s was cancelled", result.RunID)
	default:
		return nil
	}
}

// resolveRevisionArg matches an exact revision id first, then a unique
// checksum prefix.
func resolveRevisionArg(revisions []client.RevisionSummary, arg string) (*client.RevisionSummary, error) {
	for index := range revisions {
		if revisions[index].ID == arg {
			return &revisions[index], nil
		}
	}
	needle := strings.TrimPrefix(arg, "sha256:")
	var matches []*client.RevisionSummary
	for index := range revisions {
		if strings.HasPrefix(strings.TrimPrefix(revisions[index].Checksum, "sha256:"), needle) {
			matches = append(matches, &revisions[index])
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no revision matches %s in this environment", arg)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("revision %s is ambiguous; pass more characters or the revision id", arg)
	}
}

// chooseRevision offers the environment's revisions newest first. The
// previous deploy (the newest revision strictly older than the active one)
// preselects as the default.
func chooseRevision(out io.Writer, in *bufio.Reader, revisions []client.RevisionSummary,
	pointer *client.Target) (*client.RevisionSummary, error) {
	byID := make(map[string]*client.RevisionSummary, len(revisions))
	for index := range revisions {
		byID[revisions[index].ID] = &revisions[index]
	}
	var active *client.RevisionSummary
	if pointer.ActiveRevisionID != nil {
		active = byID[*pointer.ActiveRevisionID]
	}
	defaultID := ""
	if active != nil {
		for index := range revisions {
			if revisions[index].CreatedAt.Before(active.CreatedAt) {
				defaultID = revisions[index].ID
				break
			}
		}
	}
	options := make([]cliprompt.Option, 0, len(revisions))
	for index := range revisions {
		rev := &revisions[index]
		var notes []string
		if active != nil && rev.ID == active.ID {
			notes = append(notes, "active")
		} else if pointer.TargetRevisionID != nil && *pointer.TargetRevisionID == rev.ID {
			notes = append(notes, "target")
		}
		if active != nil && rev.ID != active.ID {
			if rev.DefinitionHash != active.DefinitionHash {
				notes = append(notes, "definition changed")
			}
			if rev.ValuesHash != active.ValuesHash {
				notes = append(notes, "values changed")
			}
		}
		options = append(options, cliprompt.Option{
			Label:       utils.ShortChecksum(rev.Checksum) + "  " + utils.HumanSince(rev.CreatedAt),
			Description: strings.Join(notes, ", "),
			Value:       rev.ID,
		})
	}
	selected, err := promptSession(out, in).Select(context.Background(), cliprompt.SelectOptions{
		Title:        "Roll back to which revision?",
		Options:      options,
		DefaultValue: defaultID,
	})
	if err != nil {
		return nil, err
	}
	chosen := byID[selected]
	if chosen == nil {
		return nil, errors.New("no revision selected")
	}
	return chosen, nil
}
