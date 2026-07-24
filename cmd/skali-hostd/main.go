// Command skali-hostd is the installer-owned privileged host service used by
// reconciled managed clusters. It is intentionally separate from skalid:
// product compromise must not grant an arbitrary host-execution surface.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/hostdaemon"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	root := &cobra.Command{
		Use:           "skali-hostd",
		Short:         "Skali managed-host lifecycle service",
		Version:       versionpkg.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(&cobra.Command{
		Use:   "agent",
		Short: "Poll the coordinator and execute typed local lifecycle actions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return (&hostdaemon.Agent{Logger: logger}).Run(cmd.Context())
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "coordinator",
		Short: "Serve enrollment and reconcile the desired cluster revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return (&hostdaemon.CoordinatorDaemon{Logger: logger}).Run(cmd.Context())
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt,
		syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
