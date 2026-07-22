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

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/layout"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func newClusterUpgradeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade k3s and the Skali bundle to this installer's versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := os.Stdout
			banner(out)
			reader := bufio.NewReader(os.Stdin)
			return runUpgradeFlow(cmd.Context(), out, reader, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// runUpgradeFlow plans, confirms, and executes a version upgrade on this
// node. Servers re-run the k3s script when the pin drifted, then converge
// the bundle; agents run no bundle, so only k3s moves there. Both the
// command and the menu's upgrade entry land here.
func runUpgradeFlow(ctx context.Context, out *os.File, reader *bufio.Reader, yes bool) error {
	if _, err := darwinPrelude(ctx, out, vmPolicyMaintain, ""); err != nil {
		return err
	}
	status, err := installer.GatherStatus(ctx, runner())
	if err != nil {
		return err
	}
	detected := status.Host
	switch detected.State {
	case installer.StateServer, installer.StateAgent:
	case installer.StateUnmanaged:
		return unmanagedError()
	case installer.StateDamaged:
		return fmt.Errorf("this installation is damaged: %s; repair is not implemented in this slice",
			strings.Join(detected.Problems, "; "))
	case installer.StateUnsupported:
		return fmt.Errorf("this host cannot run a skali installation: %s",
			strings.Join(detected.Problems, "; "))
	default:
		return fmt.Errorf("no skali installation on this host (state %s); run skali cluster install first",
			detected.State)
	}
	record := detected.Record
	role := record.Node.Role
	if role == layout.RoleAgent && imageTarFlag != "" {
		return errors.New("--image-tar applies to server upgrades; agent nodes run no bundle")
	}
	if role == layout.RoleServer && record.Versions.Bundle == "" {
		return errors.New("the bundle was never initialized; run skali cluster init first")
	}

	plan := installer.PlanUpgrade(status, imageTarFlag != "")
	if plan.K3sDowngrade {
		return fmt.Errorf("this installer pins k3s %s but the host runs %s; a newer installer must run this upgrade",
			plan.K3sTo, plan.K3sFrom)
	}
	if plan.Nothing(role) {
		fmt.Fprintln(out, "already current, nothing to do")
		return nil
	}

	// Every input is settled before anything prints or mutates, so the
	// plan can name the image and a refused prompt aborts cleanly.
	promptAllowed := !yes && cliprompt.Interactive()
	opts := installer.InitOptions{Out: out, SkipAdmin: true}
	var tarData []byte
	if role == layout.RoleServer {
		if err := seedInitInputs(reader, promptAllowed, record, &opts); err != nil {
			return err
		}
		if imageTarFlag != "" {
			tarData, opts.SkalidImage, opts.SkalidImageID, err = loadImageTar(ctx, imageTarFlag)
			if err != nil {
				return err
			}
		} else if err := resolveSkalidImage(reader, promptAllowed, &opts); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "upgrade plan for host %s (cluster %q)\n", hostLabel(detected), record.Cluster)
	printUpgradePlan(out, plan, role, opts.SkalidImage, tarData != nil)

	if !yes {
		if !cliprompt.Interactive() {
			return errors.New("non-interactive run requires --yes")
		}
		if !cliprompt.Confirm(reader, "Continue? [y/N] ") {
			return errors.New("upgrade cancelled; nothing was changed")
		}
		fmt.Fprintln(out)
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)

	if role == layout.RoleAgent {
		if err := installer.UpgradeAgent(ctx, runner(), record, progress); err != nil {
			progress.Abort()
			return err
		}
		progress.Done("")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "upgrade complete:")
		fmt.Fprintf(out, "  k3s  %s\n", installer.K3sVersion)
		return nil
	}

	if plan.K3sDrifted {
		if err := installer.UpgradeK3s(ctx, runner(), record, progress); err != nil {
			progress.Abort()
			return err
		}
	}
	if tarData != nil {
		if err := importImageTar(ctx, runner(), tarData, opts.SkalidImage, progress); err != nil {
			progress.Abort()
			return err
		}
	}
	opts.Progress = progress
	result, err := installer.Init(ctx, runner(), record, opts)
	if err != nil {
		progress.Abort()
		return err
	}
	progress.Done("")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "upgrade complete:")
	fmt.Fprintf(out, "  k3s     %s\n", installer.K3sVersion)
	fmt.Fprintf(out, "  bundle  %s\n", versionpkg.Version)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Install logs:", result.LogPath)
	return nil
}

// printUpgradePlan renders the drift lines the confirmation refers to. A
// server converges whenever the flow reaches this point, so the bundle
// line always states what the converge is for.
func printUpgradePlan(out io.Writer, plan installer.UpgradePlan, role, image string, fromTar bool) {
	if plan.K3sDrifted {
		fmt.Fprintf(out, "  k3s     %s -> %s\n", orUnknown(plan.K3sFrom), plan.K3sTo)
	} else {
		fmt.Fprintf(out, "  k3s     %s (current)\n", plan.K3sTo)
	}
	if role == layout.RoleAgent {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "This node is an agent: upgrade the server first, then run upgrade on each node.")
		fmt.Fprintln(out)
		return
	}
	switch {
	case plan.BundleDrifted:
		fmt.Fprintf(out, "  bundle  %s -> %s\n", plan.BundleFrom, plan.BundleTo)
	case plan.ImageForced:
		fmt.Fprintf(out, "  bundle  %s (reconverge for the new skalid image)\n", plan.BundleTo)
	default:
		fmt.Fprintf(out, "  bundle  %s (reconverge to republish the record)\n", plan.BundleTo)
	}
	if image != "" {
		suffix := ""
		if fromTar {
			suffix = " (imported from tar)"
		}
		fmt.Fprintf(out, "  skalid  %s%s\n", image, suffix)
	}
	fmt.Fprintln(out)
}
