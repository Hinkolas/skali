package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
)

// newBackupCommand groups the manual backup surface: the admin-configured
// external S3 target plus snapshot creation, listing, and restore.
func newBackupCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "backup",
		Short: "Back up and restore environment data",
	}
	command.AddCommand(newBackupTargetCommand(), newBackupCreateCommand(),
		newBackupLsCommand(), newBackupRestoreCommand())
	return command
}

func newBackupRestoreCommand() *cobra.Command {
	var (
		environment string
		remote      string
		yes         bool
	)
	command := &cobra.Command{
		Use:   "restore SNAPSHOT-ID",
		Short: "Restore a snapshot's data into the environment",
		Long: "Stops the environment, replaces every matching database, bucket,\n" +
			"and volume with the snapshot's data, then resumes the current\n" +
			"revision. Current data is overwritten. The environment must be\n" +
			"deployed first; restore moves data, not configuration.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			snapshotID := args[0]
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			if !yes {
				if !cliprompt.Interactive() {
					return errors.New("non-interactive use requires --yes")
				}
				session := promptSession(out, bufio.NewReader(command.InOrStdin()))
				confirmed, err := session.ConfirmTyped(ctx,
					fmt.Sprintf("Restore snapshot %s into %s?", snapshotID, target.environment),
					"The environment stops, its current data is replaced with the "+
						"snapshot's, and the current revision resumes.",
					target.environment)
				if err != nil {
					return err
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			runID, err := target.api.RestoreBackup(ctx, target.environmentID, snapshotID)
			if isReauthRequired(err) {
				if err = reauthForAdmin(ctx, command, out, target.api); err != nil {
					return err
				}
				runID, err = target.api.RestoreBackup(ctx, target.environmentID, snapshotID)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s  restore %s into %s\n", style.Dim("run"),
				style.Bold(runID), snapshotID, target.environment)
			status, err := attachRun(ctx, out, target.api, runID, remote)
			if err != nil {
				return err
			}
			switch status {
			case "succeeded":
				fmt.Fprintf(out, "\n%s%s\n", style.Check(), style.Bold(style.Green("restore complete")))
				return nil
			case "failed":
				return fmt.Errorf("run %s failed; the environment stays down until a restore succeeds or it is redeployed", runID)
			case "cancelled":
				return fmt.Errorf("run %s was cancelled; the environment stays down until a restore succeeds or it is redeployed", runID)
			default:
				return nil
			}
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment to restore; defaults to the checkout binding")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().BoolVar(&yes, "yes", false, "skip the typed confirmation")
	return command
}

func newBackupCreateCommand() *cobra.Command {
	var (
		environment string
		remote      string
		detach      bool
	)
	command := &cobra.Command{
		Use:   "create",
		Short: "Snapshot the environment's data to the backup target",
		Long: "Backs up every database, bucket, and application volume of the\n" +
			"environment to the configured S3 target as one complete snapshot.\n" +
			"Configuration and secret values are not included: a snapshot\n" +
			"restores data into a redeployed environment.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			result, err := target.api.CreateBackup(ctx, target.environmentID)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s  back up %s\n", style.Dim("run"),
				style.Bold(result.RunID), target.environment)
			if detach {
				fmt.Fprintf(out, "backup continues on the server; attach with: %s\n", runAttachHint(remote, result.RunID))
				return nil
			}
			status, err := attachRun(ctx, out, target.api, result.RunID, remote)
			if err != nil {
				return err
			}
			switch status {
			case "succeeded":
				fmt.Fprintf(out, "\n%s%s  snapshot %s\n", style.Check(),
					style.Bold(style.Green("backup complete")), result.BackupID)
				return nil
			case "failed":
				return fmt.Errorf("run %s failed", result.RunID)
			case "cancelled":
				return fmt.Errorf("run %s was cancelled", result.RunID)
			default:
				return nil
			}
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment to back up; defaults to the checkout binding")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().BoolVar(&detach, "detach", false, "start the backup and return without following it")
	return command
}

func newBackupLsCommand() *cobra.Command {
	var environment, remote string
	command := &cobra.Command{
		Use:   "ls",
		Short: "List the environment's snapshots on the backup target",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			target, err := resolveQueryTarget(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			snapshots, err := target.api.ListBackups(ctx, target.environmentID)
			if err != nil {
				return err
			}
			if len(snapshots) == 0 {
				fmt.Fprintln(out, style.Dim("no snapshots"))
				return nil
			}
			for _, snapshot := range snapshots {
				fmt.Fprintf(out, "%s  %s  %s  %s  %s\n",
					snapshot.ID,
					snapshot.CreatedAt,
					shortChecksum(snapshot.RevisionChecksum),
					fmt.Sprintf("%dd/%db/%dv", snapshot.Databases, snapshot.Buckets, snapshot.Volumes),
					formatBytes(snapshot.Bytes))
			}
			return nil
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "environment to list; defaults to the checkout binding")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	return command
}

// formatBytes renders a byte count for humans without dropping to zero for
// small snapshots.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func newBackupTargetCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "target",
		Short: "Manage the external S3 backup target",
	}
	command.AddCommand(newBackupTargetSetCommand(), newBackupTargetShowCommand())
	return command
}

func newBackupTargetSetCommand() *cobra.Command {
	var (
		remote    string
		endpoint  string
		region    string
		bucket    string
		prefix    string
		accessKey string
		secretKey string
	)
	command := &cobra.Command{
		Use:   "set",
		Short: "Configure where backups are written",
		Long: "Stores the external S3 location backups are written to. Admin only;\n" +
			"the secret access key is write-only and never shown again. Without\n" +
			"--secret-key the key is prompted (or read from\n" +
			"SKALI_BACKUP_SECRET_KEY).",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			if secretKey == "" {
				secretKey = os.Getenv("SKALI_BACKUP_SECRET_KEY")
			}
			if secretKey == "" {
				if !cliprompt.Interactive() {
					return errors.New("--secret-key or SKALI_BACKUP_SECRET_KEY is required in non-interactive use")
				}
				session := promptSession(out, bufio.NewReader(command.InOrStdin()))
				secretKey, err = session.Secret(ctx, cliprompt.SecretOptions{Title: "Secret access key"})
				if err != nil {
					return err
				}
			}
			input := client.BackupTargetInput{
				Endpoint:        endpoint,
				Region:          region,
				Bucket:          bucket,
				Prefix:          prefix,
				AccessKeyID:     accessKey,
				SecretAccessKey: secretKey,
			}
			target, err := api.PutBackupTarget(ctx, input)
			if isReauthRequired(err) {
				if err = reauthForAdmin(ctx, command, out, api); err != nil {
					return err
				}
				target, err = api.PutBackupTarget(ctx, input)
			}
			if err != nil {
				return err
			}
			style := clirender.StyleFor(out)
			fmt.Fprintf(out, "%sbackup target set to %s/%s\n", style.Check(), target.Endpoint, target.Bucket)
			return nil
		},
	}
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().StringVar(&endpoint, "endpoint", "", "S3 endpoint URL, e.g. https://s3.example.com")
	command.Flags().StringVar(&region, "region", "", "S3 region; empty for providers that ignore it")
	command.Flags().StringVar(&bucket, "bucket", "", "bucket backups are written into")
	command.Flags().StringVar(&prefix, "prefix", "", "key prefix inside the bucket")
	command.Flags().StringVar(&accessKey, "access-key", "", "S3 access key id")
	command.Flags().StringVar(&secretKey, "secret-key", "", "S3 secret access key (prefer the prompt or SKALI_BACKUP_SECRET_KEY)")
	_ = command.MarkFlagRequired("endpoint")
	_ = command.MarkFlagRequired("bucket")
	_ = command.MarkFlagRequired("access-key")
	return command
}

func newBackupTargetShowCommand() *cobra.Command {
	var remote string
	command := &cobra.Command{
		Use:   "show",
		Short: "Show the configured backup target",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			target, err := api.GetBackupTarget(ctx)
			if isReauthRequired(err) {
				if err = reauthForAdmin(ctx, command, out, api); err != nil {
					return err
				}
				target, err = api.GetBackupTarget(ctx)
			}
			if err != nil {
				if isNotFound(err) {
					return errors.New("no backup target configured; run skali backup target set")
				}
				return err
			}
			style := clirender.StyleFor(out)
			fmt.Fprintf(out, "endpoint    %s\n", target.Endpoint)
			if target.Region != "" {
				fmt.Fprintf(out, "region      %s\n", target.Region)
			}
			fmt.Fprintf(out, "bucket      %s\n", target.Bucket)
			if target.Prefix != "" {
				fmt.Fprintf(out, "prefix      %s\n", target.Prefix)
			}
			fmt.Fprintf(out, "access key  %s\n", target.AccessKeyID)
			fmt.Fprintln(out, style.Dim("the secret access key is write-only and never shown"))
			return nil
		},
	}
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	return command
}

// reauthForAdmin refreshes the sudo window for an admin-gated call: the
// local platform reuses its recorded bootstrap password, every other remote
// prompts for the account password.
func reauthForAdmin(ctx context.Context, command *cobra.Command, out io.Writer, api *client.Client) error {
	if api.Master() == localdev.MasterURL() {
		return reauthLocal(ctx, api)
	}
	if !cliprompt.Interactive() {
		return errors.New("recent authentication required; re-run in a terminal to confirm your password")
	}
	session := promptSession(out, bufio.NewReader(command.InOrStdin()))
	password, err := session.Secret(ctx, cliprompt.SecretOptions{Title: "Confirm your password"})
	if err != nil {
		return err
	}
	if err := api.Reauthenticate(ctx, password); err != nil {
		return fmt.Errorf("reauthenticate: %w", err)
	}
	return nil
}
