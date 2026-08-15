package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/utils"
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
		Short: "Restore a snapshot's data into an environment",
		Long: "Stops the environment, replaces every matching database, bucket,\n" +
			"and volume with the snapshot's data, then resumes the current\n" +
			"revision. Current data is overwritten. The environment must be\n" +
			"deployed first; restore moves data, not configuration.\n\n" +
			"The snapshot restores into the environment it was taken from unless\n" +
			"--environment names another environment of the same project.",
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
			scope, err := resolveQueryProject(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			snapshot, err := findSnapshot(ctx, scope, snapshotID)
			if err != nil {
				return err
			}
			targetEnvironment, err := restoreEnvironment(scope, snapshot, environment)
			if err != nil {
				return err
			}
			if !yes {
				if !cliprompt.Interactive() {
					return errors.New("non-interactive use requires --yes")
				}
				session := promptSession(out, bufio.NewReader(command.InOrStdin()))
				confirmed, err := session.ConfirmTyped(ctx,
					fmt.Sprintf("Restore snapshot %s into %s?", snapshotID, targetEnvironment.Name),
					fmt.Sprintf("The snapshot was taken from %s on %s. The environment stops, its "+
						"current data is replaced with the snapshot's, and the current "+
						"revision resumes.", snapshot.Environment, snapshotTime(snapshot)),
					targetEnvironment.Name)
				if err != nil {
					return err
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			runID, err := scope.api.RestoreBackup(ctx, targetEnvironment.ID, snapshotID)
			if isReauthRequired(err) {
				if err = reauthForAdmin(ctx, command, out, scope.api); err != nil {
					return err
				}
				runID, err = scope.api.RestoreBackup(ctx, targetEnvironment.ID, snapshotID)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s  restore %s (%s) into %s\n", style.Dim("run"),
				style.Bold(runID), snapshotID, snapshot.Environment, targetEnvironment.Name)
			status, err := attachRun(ctx, out, scope.api, runID, remote)
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
	command.Flags().StringVar(&environment, "environment", "",
		"environment to restore into; defaults to the environment the snapshot was taken from")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	command.Flags().BoolVar(&yes, "yes", false, "skip the typed confirmation")
	return command
}

// findSnapshot locates a snapshot by id among the project's snapshots on
// the backup target.
func findSnapshot(ctx context.Context, scope *queryProject, snapshotID string) (*client.BackupSnapshot, error) {
	snapshots, err := scope.api.ListProjectBackups(ctx, scope.project.ID)
	if err != nil {
		return nil, err
	}
	for i := range snapshots {
		if snapshots[i].ID == snapshotID {
			return &snapshots[i], nil
		}
	}
	return nil, fmt.Errorf("no snapshot %s in project %s on %s; see skali backup ls",
		snapshotID, scope.project.Name, scope.api.Master())
}

// restoreEnvironment picks the environment a snapshot restores into: the
// explicitly named one, else the environment the snapshot was taken from.
// Either must currently exist in the project.
func restoreEnvironment(scope *queryProject, snapshot *client.BackupSnapshot, explicit string) (*client.Environment, error) {
	if explicit != "" {
		if resolved := findEnvironment(scope.environments, explicit); resolved != nil {
			return resolved, nil
		}
		return nil, fmt.Errorf("environment %s does not exist in project %s on %s",
			explicit, scope.project.Name, scope.api.Master())
	}
	if resolved := findEnvironment(scope.environments, snapshot.Environment); resolved != nil {
		return resolved, nil
	}
	return nil, fmt.Errorf("environment %s, which the snapshot was taken from, does not exist in project %s on %s; "+
		"deploy it again or pass --environment to restore into another environment",
		snapshot.Environment, scope.project.Name, scope.api.Master())
}

// snapshotTime renders a snapshot's creation time as a local timestamp,
// falling back to the raw value the API returned.
func snapshotTime(snapshot *client.BackupSnapshot) string {
	created, err := time.Parse(time.RFC3339, snapshot.CreatedAt)
	if err != nil {
		return snapshot.CreatedAt
	}
	return created.Local().Format("2006-01-02 15:04")
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
		Short: "List the project's snapshots on the backup target",
		Long: "Lists the snapshots of every environment of the project, newest\n" +
			"first, as the backup target holds them; --environment narrows the\n" +
			"list to one environment. Snapshots of environments that no longer\n" +
			"exist are listed too and can be restored into another environment.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			start, err := os.Getwd()
			if err != nil {
				return err
			}
			scope, err := resolveQueryProject(ctx, start, environment, remote)
			if err != nil {
				return err
			}
			snapshots, err := scope.api.ListProjectBackups(ctx, scope.project.ID)
			if err != nil {
				return err
			}
			if environment != "" {
				snapshots = slices.DeleteFunc(snapshots, func(snapshot client.BackupSnapshot) bool {
					return snapshot.Environment != environment
				})
			}
			fmt.Fprintf(out, "%s  %s %s\n\n", style.Dim("project"),
				scope.project.Name, style.Dim("("+scope.remoteName+")"))
			if len(snapshots) == 0 {
				if environment != "" {
					fmt.Fprintln(out, style.Dim("no snapshots of environment "+environment))
				} else {
					fmt.Fprintln(out, style.Dim("no snapshots"))
				}
				return nil
			}
			renderSnapshotTable(out, snapshots)
			return nil
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "only list snapshots of this environment")
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
	return command
}

// renderSnapshotTable prints snapshots as one table with a header row: the
// id first (it is what restore takes), then where and when the snapshot was
// taken, the revision that was running, and what it holds.
func renderSnapshotTable(out io.Writer, snapshots []client.BackupSnapshot) {
	environmentWidth := len("ENVIRONMENT")
	for _, snapshot := range snapshots {
		environmentWidth = max(environmentWidth, len(snapshot.Environment))
	}
	row := func(id, environment, created, revision, databases, buckets, volumes, size string) {
		fmt.Fprintf(out, "%-36s  %-*s  %-16s  %-12s  %-9s  %-7s  %-7s  %s\n",
			id, environmentWidth, environment, created, revision, databases, buckets, volumes, size)
	}
	row("SNAPSHOT", "ENVIRONMENT", "CREATED", "REVISION", "DATABASES", "BUCKETS", "VOLUMES", "SIZE")
	for i := range snapshots {
		snapshot := &snapshots[i]
		row(snapshot.ID, snapshot.Environment, snapshotTime(snapshot),
			utils.ShortChecksum(snapshot.RevisionChecksum),
			strconv.Itoa(snapshot.Databases), strconv.Itoa(snapshot.Buckets), strconv.Itoa(snapshot.Volumes),
			utils.FormatBytes(snapshot.Bytes))
	}
}

func newBackupTargetCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "target",
		Short: "Manage the external S3 backup target",
	}
	command.AddCommand(newBackupTargetSetCommand(), newBackupTargetShowCommand(),
		newBackupTargetUnsetCommand())
	return command
}

func newBackupTargetUnsetCommand() *cobra.Command {
	var remote string
	command := &cobra.Command{
		Use:   "unset",
		Short: "Remove the configured backup target",
		Long: "Removes the backup target and its stored credentials. Admin only.\n" +
			"Snapshots already written to the bucket are untouched; setting the\n" +
			"same target again makes them listable and restorable once more.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			api, err := queryClient(remote)
			if err != nil {
				return err
			}
			err = api.DeleteBackupTarget(ctx)
			if isReauthRequired(err) {
				if err = reauthForAdmin(ctx, command, out, api); err != nil {
					return err
				}
				err = api.DeleteBackupTarget(ctx)
			}
			if err != nil {
				if isNotFound(err) {
					return errors.New("no backup target configured")
				}
				return err
			}
			style := clirender.StyleFor(out)
			fmt.Fprintf(out, "%sbackup target removed\n", style.Check())
			return nil
		},
	}
	command.Flags().StringVar(&remote, "remote", "",
		"remote to target for this one invocation, ignoring the checkout binding and the current remote")
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
