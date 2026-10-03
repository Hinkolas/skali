package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/utils"
)

func newBucketCommand() *cobra.Command {
	command := &cobra.Command{
		Use:               "bucket",
		Short:             "Operate the environment's managed buckets",
		ValidArgsFunction: cobra.NoFileCompletions,
		Long: "Managed object storage buckets of an environment. Buckets are declared\n" +
			"in skali.yml; this group holds the operations on a provisioned bucket\n" +
			"that are not configuration.",
	}
	command.AddCommand(newBucketRotateCommand())
	return command
}

func newBucketRotateCommand() *cobra.Command {
	var (
		environment string
		remote      string
		retireAfter string
		detach      bool
		yes         bool
	)
	command := &cobra.Command{
		Use:   "rotate <bucket>",
		Short: "Issue a new S3 keypair for a bucket and retire the current one",
		Long: "Issues a new keypair for the bucket named by its manifest key. The\n" +
			"store accepts both keypairs, the applications referencing the bucket\n" +
			"restart with the new one, and after the overlap window the previous\n" +
			"keypair is retired for good: requests and presigned URLs signed with\n" +
			"it fail from that instant on, so --retire-after must cover the longest\n" +
			"URL the application issues (1h by default, 1m for a key known to be\n" +
			"leaked, 7d at most). Processes holding the old keypair outside the\n" +
			"cluster (skali dev host runs, anyone who revealed it) must fetch it\n" +
			"again. Rotating again inside the window retires the older keypair at\n" +
			"once. A bucket a restore is rewriting cannot be rotated until the\n" +
			"restore has finished.\n\n" +
			"The run streams like a deploy and succeeds once every consumer runs\n" +
			"with the new keypair; the previous keypair retires on schedule either\n" +
			"way. Needs maintain on the environment and a recent login.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeBucketArg,
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			in := bufio.NewReader(command.InOrStdin())
			window, err := parseRetireAfter(retireAfter)
			if err != nil {
				return fmt.Errorf("--retire-after %q: %w", retireAfter, err)
			}
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			key := args[0]
			printHeader(out, style,
				headerRow{"remote", target.remoteName, target.master},
				headerRow{"project", target.project, ""},
				headerRow{"environment", target.environment, ""},
				headerRow{"bucket", key, "previous key retires after " + describeWindow(window)})
			if !yes {
				confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
					Title: fmt.Sprintf("Rotate the keypair of bucket %s in %s?", key, target.environment),
					Description: fmt.Sprintf("A new keypair is issued and the applications using buckets.%s restart "+
						"with it. Requests and URLs signed with the current keypair keep working for %s, then fail.",
						key, describeWindow(window)),
					Default: true,
				})
				if err != nil {
					return confirmError(err)
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			seconds := int64(window / time.Second)
			runID, err := target.api.RotateBucketCredentials(ctx, target.environmentID, key, seconds)
			if isReauthRequired(err) {
				if err = reauthSession(ctx, out, in, target.api); err != nil {
					return err
				}
				runID, err = target.api.RotateBucketCredentials(ctx, target.environmentID, key, seconds)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s  rotate the keypair of buckets.%s in %s\n", style.Dim("run"),
				style.Bold(runID), key, target.environment)
			if detach {
				fmt.Fprintf(out, "the rotation continues on the server; attach with: %s\n", runAttachHint(remote, runID))
				return nil
			}
			outcome, err := attachRun(ctx, out, target.api, runID, remote)
			if err != nil {
				return err
			}
			switch outcome.Status {
			case "succeeded":
				detail := ""
				if connection, err := target.api.BucketConnection(ctx, target.environmentID, key); err == nil {
					detail = fmt.Sprintf("  credentials v%d", connection.CredentialVersion)
					if connection.CredentialRetireAt != nil {
						detail += " · previous key retires at " + connection.CredentialRetireAt.Local().Format("2006-01-02 15:04")
					}
				}
				fmt.Fprintf(out, "\n%s%s%s\n", style.Check(), style.Bold(style.Green("keypair rotated")), detail)
				return nil
			case "failed":
				return fmt.Errorf("%w; the new keypair stands and the previous one retires on schedule",
					failedRunError(runID, outcome.Failure))
			case "cancelled":
				return fmt.Errorf("run %s was cancelled; the new keypair stands and the previous one retires on schedule", runID)
			default:
				return nil
			}
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment holding the bucket; defaults to the checkout binding")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().StringVar(&retireAfter, "retire-after", "1h",
		"how long the previous keypair stays accepted, such as 30m, 1h, or 2d (at least 1m, at most 7d)")
	command.Flags().BoolVar(&detach, "detach", false, "start the rotation and return without following it")
	command.Flags().BoolVar(&yes, "yes", false, "skip the confirmation")
	return command
}

// completeBucketArg completes the manifest's bucket keys as the sole
// positional, read without compiling so a half-edited manifest still
// completes what it declares.
func completeBucketArg(_ *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return noCompletions()
	}
	var values []cobra.Completion
	for _, key := range utils.SortedKeys(manifestBuckets()) {
		values = append(values, cobra.Completion(key))
	}
	return filterCompletions(values, toComplete)
}

func manifestBuckets() map[string]manifest.Bucket {
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
	return document.Project.Buckets
}
