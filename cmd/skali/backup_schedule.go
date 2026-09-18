package main

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/client"
	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/cron"
)

// newBackupScheduleCommand configures automatic backups of one
// environment: a cron schedule (UTC) and the retention of the snapshots it
// takes. It is an environment setting, changed through the same settings
// route as the ceiling, the deploy policy, and the priority.
func newBackupScheduleCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "schedule",
		Short: "Configure automatic backups of an environment",
		Long: "Automatic backups are an environment setting: every database, bucket,\n" +
			"and volume of the environment is snapshotted on a cron schedule (UTC),\n" +
			"and snapshots the schedule takes are deleted once older than the\n" +
			"retention (the newest is always kept). Manual snapshots never expire.\n" +
			"Changing the schedule needs environment admin and a recent login.",
	}
	command.AddCommand(newBackupScheduleSetCommand(), newBackupScheduleShowCommand(), newBackupScheduleRemoveCommand())
	return command
}

func newBackupScheduleSetCommand() *cobra.Command {
	var project, environment, remote, every, keep string
	var yes bool
	command := &cobra.Command{
		Use:   "set",
		Short: "Turn automatic backups on or change their schedule",
		Long: "Sets the automatic backup schedule of the bound environment (or\n" +
			"--environment): --every is a five-field cron expression evaluated in\n" +
			"UTC, --keep how long the snapshots it takes are kept (7d, 4w, 12h; at\n" +
			"least one minute). The first snapshot runs at the next fire of the\n" +
			"schedule, never right away.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			in := bufio.NewReader(command.InOrStdin())
			style := clirender.StyleFor(out)
			schedule, err := cron.Parse(every)
			if err != nil {
				return fmt.Errorf("--every %q: %w", every, err)
			}
			retention, err := parseKeep(keep)
			if err != nil {
				return fmt.Errorf("--keep %q: %w", keep, err)
			}
			scope, err := resolveEnvScope(ctx, project, environment, remote)
			if err != nil {
				return err
			}
			if scope.environment.Settings == nil {
				return fmt.Errorf("environment %s is locked for you; read on it is required to see its settings", scope.environment.Name)
			}
			next := &client.BackupSchedule{Schedule: schedule.String(), RetentionSeconds: retention}
			printHeader(out, style,
				headerRow{"remote", scope.remoteName, scope.api.Master()},
				headerRow{"project", scope.project.Name, ""},
				headerRow{"environment", scope.environment.Name, ""})
			fmt.Fprintf(out, "  %-14s %s -> %s\n", "backups",
				describeBackupScheduleLong(scope.environment.Settings.Backup), describeBackupScheduleLong(next))
			if !yes {
				confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
					Title: fmt.Sprintf("Schedule automatic backups of %s?", scope.environment.Name),
					Description: fmt.Sprintf("Every database, bucket, and volume is snapshotted %s UTC; "+
						"snapshots the schedule takes expire after %s, the newest is always kept.",
						cron.Describe(next.Schedule), describeRetention(retention)),
					Default: true,
				})
				if err != nil {
					return confirmError(err)
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			var updated *client.Environment
			err = withReauth(ctx, out, in, scope.api, func() (err error) {
				updated, err = scope.api.UpdateEnvironmentSettings(ctx, scope.environment.ID,
					client.EnvironmentSettingsPatch{Backup: client.BackupPatch(next)})
				return err
			})
			if err != nil {
				return err
			}
			printEnvironmentSettings(out, scope, updated)
			fire := schedule.Next(time.Now())
			fmt.Fprintln(out, style.Dim("next backup "+fire.Local().Format("2006-01-02 15:04")+
				" local ("+fire.UTC().Format("2006-01-02 15:04")+" UTC)"))
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	command.Flags().StringVar(&environment, "environment", "", "environment to schedule; defaults to the checkout binding")
	command.Flags().StringVar(&every, "every", "", "five-field cron expression in UTC, for example \"0 3 * * *\"")
	command.Flags().StringVar(&keep, "keep", "", "how long snapshots the schedule takes are kept, for example 7d or 4w")
	command.Flags().BoolVar(&yes, "yes", false, "skip the confirmation")
	_ = command.MarkFlagRequired("every")
	_ = command.MarkFlagRequired("keep")
	return command
}

func newBackupScheduleShowCommand() *cobra.Command {
	var project, environment, remote string
	command := &cobra.Command{
		Use:   "show",
		Short: "Show the automatic backup schedule of an environment",
		Long: "Prints whether automatic backups of the bound environment (or\n" +
			"--environment) are on, their schedule, retention, and next fire.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			out := command.OutOrStdout()
			style := clirender.StyleFor(out)
			scope, err := resolveEnvScope(command.Context(), project, environment, remote)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "environment    %s %s\n", scope.environment.Name,
				style.Dim("("+scope.project.Name+" on "+scope.remoteName+")"))
			if scope.environment.Settings == nil {
				return fmt.Errorf("environment %s is locked for you; read on it is required to see its settings", scope.environment.Name)
			}
			backup := scope.environment.Settings.Backup
			if backup == nil {
				fmt.Fprintln(out, "backups        off")
				fmt.Fprintln(out, style.Dim("turn them on with skali backup schedule set --every \"0 3 * * *\" --keep 7d"))
				return nil
			}
			fmt.Fprintf(out, "schedule       %s %s\n", cron.Describe(backup.Schedule)+" UTC", style.Dim("("+backup.Schedule+")"))
			fmt.Fprintf(out, "keep           %s\n", describeRetention(backup.RetentionSeconds))
			if schedule, err := cron.Parse(backup.Schedule); err == nil {
				fire := schedule.Next(time.Now())
				fmt.Fprintf(out, "next           %s %s\n", fire.Local().Format("2006-01-02 15:04"),
					style.Dim("("+fire.UTC().Format("2006-01-02 15:04")+" UTC)"))
			}
			fmt.Fprintln(out, style.Dim("snapshots: skali backup list --environment "+scope.environment.Name))
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	command.Flags().StringVar(&environment, "environment", "", "environment to show; defaults to the checkout binding")
	return command
}

func newBackupScheduleRemoveCommand() *cobra.Command {
	var project, environment, remote string
	var yes bool
	command := &cobra.Command{
		Use:     "remove",
		Aliases: []string{"rm"},
		Short:   "Turn automatic backups of an environment off",
		Long: "Turns automatic backups of the bound environment (or --environment)\n" +
			"off. Snapshots already taken stay on the backup target and no longer\n" +
			"expire; manual backups keep working.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			out := command.OutOrStdout()
			in := bufio.NewReader(command.InOrStdin())
			style := clirender.StyleFor(out)
			scope, err := resolveEnvScope(ctx, project, environment, remote)
			if err != nil {
				return err
			}
			if scope.environment.Settings == nil {
				return fmt.Errorf("environment %s is locked for you; read on it is required to see its settings", scope.environment.Name)
			}
			if scope.environment.Settings.Backup == nil {
				fmt.Fprintf(out, "automatic backups of %s are already off\n", scope.environment.Name)
				return nil
			}
			printHeader(out, style,
				headerRow{"remote", scope.remoteName, scope.api.Master()},
				headerRow{"project", scope.project.Name, ""},
				headerRow{"environment", scope.environment.Name, ""})
			fmt.Fprintf(out, "  %-14s %s -> off\n", "backups", describeBackupScheduleLong(scope.environment.Settings.Backup))
			if !yes {
				confirmed, err := promptSession(out, in).Confirm(ctx, cliprompt.ConfirmOptions{
					Title:       fmt.Sprintf("Turn off automatic backups of %s?", scope.environment.Name),
					Description: "Existing snapshots stay on the backup target and no longer expire; manual backups still work.",
				})
				if err != nil {
					return confirmError(err)
				}
				if !confirmed {
					return errors.New("aborted")
				}
			}
			err = withReauth(ctx, out, in, scope.api, func() error {
				_, err := scope.api.UpdateEnvironmentSettings(ctx, scope.environment.ID,
					client.EnvironmentSettingsPatch{Backup: client.BackupPatch(nil)})
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s automatic backups off  %s\n", style.Check(), style.Dim("environment "+scope.environment.Name))
			return nil
		},
	}
	addAccessScopeFlags(command, &project, &remote)
	command.Flags().StringVar(&environment, "environment", "", "environment to change; defaults to the checkout binding")
	command.Flags().BoolVar(&yes, "yes", false, "skip the confirmation")
	return command
}

// parseKeep reads a retention such as 7d, 4w, or 12h into whole seconds.
func parseKeep(text string) (int64, error) {
	milliseconds, err := compiler.ParseDuration(strings.TrimSpace(text))
	if err != nil {
		return 0, err
	}
	if milliseconds <= 0 || milliseconds%1000 != 0 {
		return 0, errors.New("must be a positive duration in whole seconds such as 12h, 7d, or 4w")
	}
	return milliseconds / 1000, nil
}

// describeBackupSchedule words a schedule for tables: "off", or "daily at
// 03:00 UTC, keep 7d".
func describeBackupSchedule(settings *client.EnvironmentSettings) string {
	if settings == nil || settings.Backup == nil {
		return "off"
	}
	return cron.Describe(settings.Backup.Schedule) + " UTC, keep " + shortRetention(settings.Backup.RetentionSeconds)
}

// describeBackupScheduleLong words a schedule for settings lines: "off", or
// "daily at 03:00 UTC, keep 7 days".
func describeBackupScheduleLong(backup *client.BackupSchedule) string {
	if backup == nil {
		return "off"
	}
	return cron.Describe(backup.Schedule) + " UTC, keep " + describeRetention(backup.RetentionSeconds)
}

// describeRetention words a retention window in its largest exact unit:
// "7 days", "36 hours", "2 minutes".
func describeRetention(seconds int64) string {
	unit := func(n int64, word string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, word)
		}
		return fmt.Sprintf("%d %ss", n, word)
	}
	switch {
	case seconds%86400 == 0:
		return unit(seconds/86400, "day")
	case seconds%3600 == 0:
		return unit(seconds/3600, "hour")
	default:
		return unit((seconds+30)/60, "minute")
	}
}

// shortRetention is describeRetention for table cells: 7d, 36h, 2m.
func shortRetention(seconds int64) string {
	switch {
	case seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dm", (seconds+30)/60)
	}
}
