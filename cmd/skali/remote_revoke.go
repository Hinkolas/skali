package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Hinkolas/skali/internal/cliconfig"
	"github.com/Hinkolas/skali/internal/client"
	versionpkg "github.com/Hinkolas/skali/internal/version"
	"github.com/spf13/cobra"
)

// Forgetting local credentials remains possible offline. Server revocation,
// when reachable, must use the matching release; failure never runs another
// release's API body as a substitute.
func revokeRemoteSession(ctx context.Context, cfg *cliconfig.Config, name string, remote *cliconfig.Remote) error {
	if versionpkg.IsRelease(versionpkg.Version) {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		probe := client.New(remote.Master, "", caller())
		probe.PinInstance(remote.Instance, nil)
		if err := probe.Health(probeCtx); err != nil {
			return err
		}
		d, err := newDispatcher([]string{"remote", "revoke-session", name})
		if err != nil {
			return err
		}
		if handled, code := d.handoff(ctx, name, remote.Master, probe.ObservedVersion()); handled {
			if code != 0 {
				return fmt.Errorf("matching CLI could not revoke session (exit %d)", code)
			}
			return nil
		}
	}
	return remoteClient(cfg, remote).Logout(ctx)
}

func newRemoteRevokeCommand() *cobra.Command {
	return &cobra.Command{Use: "revoke-session <name>", Hidden: true, ValidArgsFunction: completeRemoteArg, Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		cfg, err := cliconfig.Load()
		if err != nil {
			return err
		}
		target, err := resolveRemoteTarget(cfg, "", "", args[0])
		if err != nil {
			return err
		}
		return remoteClient(cfg, target.Remote).Logout(command.Context())
	}}
}
