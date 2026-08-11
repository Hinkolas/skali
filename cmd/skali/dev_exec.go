package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func newDevExecCommand() *cobra.Command {
	var inv execInvocation
	command := &cobra.Command{
		Use:   "exec [service] [flags] -- <command>...",
		Short: "Run a command in a running app container",
		Long: "Exec opens a command or interactive shell inside a running container of\n" +
			"one service on the local platform, like docker exec. Without a command\n" +
			"it starts /bin/sh.",
		Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			service, argv, err := parseExecArgs(command, args)
			if err != nil {
				return err
			}
			if service == "" && inv.pod == "" {
				if service, err = defaultExecService(command); err != nil {
					return err
				}
			}
			api, environmentID, err := localProjectEnvironment(command)
			if err != nil {
				return err
			}
			project, err := loadLocalProject("")
			if err != nil {
				return err
			}
			reauth := func(ctx context.Context) error { return reauthLocal(ctx, api) }
			prompt := shellPrompt(localRemoteName, service, inv, project.Result.Definition.Name)
			err = runExecSession(command, api, environmentID, inv, service, argv, prompt, reauth)
			if isNoReadyPod(err) {
				// Pause-on-exit scales dev workloads to zero between
				// sessions; the pod usually just is not running yet.
				return fmt.Errorf("%w; is a dev session running? start one with skali dev", err)
			}
			return err
		},
	}
	addExecFlags(command, &inv)
	return command
}
