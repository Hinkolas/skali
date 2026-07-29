package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
)

func newClusterInitCmd() *cobra.Command {
	var configPath, layoutPath string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize Skali on the joined cluster (run once on a server)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := os.Stdout

			if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
				return err
			}
			detected, err := installer.Detect(ctx, runner())
			if err != nil {
				return err
			}
			switch detected.State {
			case installer.StateServer:
			case installer.StateUnmanaged:
				return unmanagedError()
			case installer.StateAgent:
				return fmt.Errorf("init must run on a server node; this host joined as an agent")
			case installer.StateDamaged:
				return fmt.Errorf("this installation is damaged: %s",
					strings.Join(detected.Problems, "; "))
			default:
				return fmt.Errorf("no skali installation on this host (state %s); run install first",
					detected.State)
			}

			var asserted *layout.Layout
			if layoutPath != "" {
				document, err := layout.ParseFile(layoutPath)
				if err != nil {
					return err
				}
				if diagnostics := layout.Validate(document); len(diagnostics) > 0 {
					return diagnostics
				}
				asserted = &document.Layout
			}

			if configPath == "" {
				if !cliprompt.Interactive() {
					return fmt.Errorf("non-interactive run requires --config")
				}
				banner(out)
				reader := bufio.NewReader(os.Stdin)
				return runInteractiveInit(ctx, out, reader, detected.Record, asserted)
			}

			data, err := os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", configPath, err)
			}
			config, err := installer.ParseInitConfig(data)
			if err != nil {
				return err
			}
			password, err := readHostFile(ctx, config.Admin.PasswordFile)
			if err != nil {
				return fmt.Errorf("read admin password file %s: %w", config.Admin.PasswordFile, err)
			}
			adminPassword := strings.TrimSpace(string(password))
			if adminPassword == "" {
				return fmt.Errorf("admin password file %s is empty", config.Admin.PasswordFile)
			}

			tasks := clirender.NewTasks(out)
			progress := newTaskProgress(tasks)
			opts := installer.InitOptions{
				Endpoints:     installer.Endpoints{API: config.Endpoints.API, Registry: config.Endpoints.Registry, S3: config.Endpoints.S3},
				TLS:           installer.TLSConfig{IssuerEmail: config.TLS.IssuerEmail, ACMEServer: config.TLS.ACMEServer},
				SkalidImage:   config.Skalid.Image,
				SkalidImageID: config.Skalid.ImageID,
				Layout:        asserted,
				Admin: func(context.Context) (string, string, error) {
					return config.Admin.Email, adminPassword, nil
				},
				Progress: progress,
				Out:      out,
			}
			if err := installer.ValidateInitOptions(opts); err != nil {
				progress.Abort()
				return err
			}
			if imageTarFlag != "" {
				imageTar, stagedImage, stagedID, err := loadImageTar(ctx, imageTarFlag)
				if err != nil {
					progress.Abort()
					return err
				}
				if stagedImage != config.Skalid.Image {
					progress.Abort()
					return fmt.Errorf("the image tar carries %s but the config names %s",
						stagedImage, config.Skalid.Image)
				}
				if err := importImageTar(ctx, runner(), imageTar, stagedImage, progress); err != nil {
					progress.Abort()
					return err
				}
				opts.SkalidImageID = stagedID
			}
			if err := stageReconciledLayout(ctx, detected.Record, asserted); err != nil {
				progress.Abort()
				return err
			}
			prepared, err := prepareReconciledInit(ctx, detected.Record, progress)
			if err != nil {
				progress.Abort()
				return err
			}
			if prepared != nil {
				opts.RegistryNode = prepared.RegistryNode
			}
			result, err := installer.Init(ctx, runner(), detected.Record, opts)
			if err != nil {
				progress.Abort()
				return err
			}
			if err := finishReconciledInit(ctx, prepared); err != nil {
				progress.Abort()
				return err
			}
			progress.Done("")
			printInitReady(out, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "non-interactive init configuration (init.yaml)")
	cmd.Flags().StringVar(&layoutPath, "layout", "", "cluster-layout document asserting the expected membership")
	return cmd
}
