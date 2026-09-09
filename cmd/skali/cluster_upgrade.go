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
	var yes, wait, recover bool
	var target string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update the whole managed cluster to one Skali release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := os.Stdout
			banner(out)
			reader := bufio.NewReader(os.Stdin)
			if recover {
				if imageTarFlag != "" {
					return errors.New("--recover cannot be combined with --image-tar")
				}
				return runRecoveryUpdate(cmd.Context(), out, reader, target, yes, wait)
			}
			detected, err := installer.Detect(cmd.Context(), runner())
			if err != nil {
				return err
			}
			if imageTarFlag == "" && detected.Record != nil && detected.Record.Reconciled() {
				_, _, api, err := currentClient()
				if err != nil {
					return fmt.Errorf("select and authenticate a Skali remote first: %w", err)
				}
				return runManagedUpdate(cmd.Context(), out, reader, api, target, yes, wait)
			}
			if target != "" || wait {
				return errors.New("--version and --wait require a managed cluster")
			}
			return runUpgradeFlow(cmd.Context(), out, reader, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the cluster update to complete")
	cmd.Flags().BoolVar(&recover, "recover", false, "submit directly from a controller without API activity checks")
	cmd.Flags().StringVar(&target, "version", "", "exact target release (default: latest on the configured channel)")
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
		return fmt.Errorf("this installation is damaged: %s; run skali cluster diagnose, then skali cluster repair",
			strings.Join(detected.Problems, "; "))
	case installer.StateUnsupported:
		return fmt.Errorf("this host cannot run a skali installation: %s",
			strings.Join(detected.Problems, "; "))
	default:
		return fmt.Errorf("no skali installation on this host (state %s); run skali cluster install first",
			detected.State)
	}
	record := detected.Record
	if record.Reconciled() && imageTarFlag == "" {
		_, _, api, err := currentClient()
		if err != nil {
			return fmt.Errorf("select and authenticate a Skali remote first: %w", err)
		}
		return runManagedUpdate(ctx, out, reader, api, "", yes, false)
	}
	role := record.Node.Role
	// A server whose local record never initialized the bundle is a
	// secondary server when the in-cluster record names the init owner:
	// its bundle is maintained there, so this host moves k3s only.
	// Reconciled (version-2) clusters have no init owner at all; the
	// shared coordinator maintains the bundle, so any active server may
	// inspect it and, when drifted, reconverge it.
	bundleOwner := ""
	if role == layout.RoleServer && record.Versions.Bundle == "" {
		switch {
		case record.Reconciled() && status.Initialized:
		case status.InitOwner == "":
			return errors.New("the bundle was never initialized; run skali cluster init first")
		default:
			bundleOwner = status.InitOwner
		}
	}
	if imageTarFlag != "" {
		tarFlag := "--image-tar"
		if role == layout.RoleAgent {
			return fmt.Errorf("%s applies to server upgrades; agent nodes run no bundle", tarFlag)
		}
		if bundleOwner != "" {
			return fmt.Errorf("%s applies to bundle upgrades; the bundle is maintained on %s",
				tarFlag, bundleOwner)
		}
	}

	plan := installer.PlanUpgrade(status, imageTarFlag != "")
	if plan.K3sDowngrade {
		return fmt.Errorf("this installer pins k3s %s but the host runs %s; a newer installer must run this upgrade",
			plan.K3sTo, plan.K3sFrom)
	}
	if plan.K3sMinorSkip {
		return fmt.Errorf("this installer pins k3s %s but the host runs %s; in-place upgrades cross at most one Kubernetes minor, so reinstall this cluster on the new version",
			plan.K3sTo, plan.K3sFrom)
	}
	// A host installed before the registry required authentication has no
	// node pull credential in registries.yaml; upgrade is its migration
	// path, so the gap keeps the flow going even with no version drift.
	// Secondary servers are excluded: healing mints a new credential that
	// only a converge (the init owner's) can publish in-cluster.
	credentialMissing := false
	if role == layout.RoleServer && bundleOwner == "" {
		credentialMissing, err = installer.NodePullCredentialMissing(ctx, runner())
		if err != nil {
			return err
		}
	}
	nothing := plan.Nothing(role)
	if bundleOwner != "" {
		nothing = !plan.K3sDrifted
	}
	if nothing && !credentialMissing {
		fmt.Fprintln(out, "already current, nothing to do")
		printRemainingUpgrades(out, status)
		return nil
	}

	// Every input is settled before anything prints or mutates, so the
	// plan can name the image and a refused prompt aborts cleanly.
	promptAllowed := !yes && cliprompt.Interactive()
	opts := installer.InitOptions{Out: out, SkipAdmin: true}
	var tarData []byte
	if role == layout.RoleServer && bundleOwner == "" {
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
	printUpgradePlan(out, plan, role, bundleOwner, opts.SkalidImage, tarData != nil, credentialMissing)
	printUpgradeSequence(out, status)

	if !yes {
		if !cliprompt.Interactive() {
			return errors.New("non-interactive run requires --yes")
		}
		confirmed, err := promptSession(out, reader).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: "Continue with this upgrade?",
		})
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("upgrade cancelled; nothing was changed")
		}
		fmt.Fprintln(out)
	}

	tasks := clirender.NewTasks(out)
	progress := newTaskProgress(tasks)

	if role == layout.RoleAgent || bundleOwner != "" {
		if err := installer.UpgradeNode(ctx, runner(), record, progress); err != nil {
			progress.Abort()
			return err
		}
		progress.Done("")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "upgrade complete:")
		fmt.Fprintf(out, "  k3s  %s\n", installer.K3sVersion)
		if bundleOwner != "" {
			fmt.Fprintf(out, "  bundle maintained on %s\n", bundleOwner)
		}
		printRemainingAfterUpgrade(ctx, out)
		return nil
	}

	// The credential is written before any k3s restart so containerd loads
	// it: a following k3s upgrade restarts the service anyway, otherwise
	// the heal restarts it itself.
	if credentialMissing {
		if err := installer.HealNodePullCredential(ctx, runner(), !plan.K3sDrifted, progress); err != nil {
			progress.Abort()
			return err
		}
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
	printRemainingAfterUpgrade(ctx, out)
	return nil
}

// printUpgradeSequence renders the ordered per-host plan on multi-node
// clusters: servers first, one at a time, then agents. Display only; the
// operator runs upgrade on each host.
func printUpgradeSequence(out io.Writer, status *installer.Status) {
	steps := installer.UpgradeSequence(status)
	if len(status.Nodes) < 2 || len(steps) == 0 {
		return
	}
	fmt.Fprintln(out, "cluster upgrade order (run per host, one at a time, wait for ready between servers):")
	for i, step := range steps {
		suffix := "run there: sudo skali cluster upgrade"
		if step.IsSelf {
			suffix = "(this host)"
		}
		fmt.Fprintf(out, "  %d. %-12s %-6s %s -> %s  %s\n",
			i+1, step.Name, step.Role, orUnknown(step.From), installer.K3sVersion, suffix)
	}
	fmt.Fprintln(out)
}

// printRemainingUpgrades names the still-drifted other nodes from an
// already gathered status.
func printRemainingUpgrades(out io.Writer, status *installer.Status) {
	var remaining []string
	for _, step := range installer.UpgradeSequence(status) {
		if !step.IsSelf {
			remaining = append(remaining, step.Name)
		}
	}
	if len(remaining) > 0 {
		fmt.Fprintf(out, "next: run sudo skali cluster upgrade on %s\n", strings.Join(remaining, ", then "))
	}
}

// printRemainingAfterUpgrade re-gathers the status after a completed
// upgrade so the guidance reflects what this host's upgrade changed.
func printRemainingAfterUpgrade(ctx context.Context, out io.Writer) {
	status, err := installer.GatherStatus(ctx, runner())
	if err != nil {
		return
	}
	printRemainingUpgrades(out, status)
}

// printUpgradePlan renders the drift lines the confirmation refers to. A
// bundle-owning server converges whenever the flow reaches this point, so
// its bundle line always states what the converge is for; agents and
// secondary servers move k3s only.
func printUpgradePlan(out io.Writer, plan installer.UpgradePlan, role, bundleOwner, image string, fromTar, credentialMissing bool) {
	if plan.K3sDrifted {
		fmt.Fprintf(out, "  k3s     %s -> %s\n", orUnknown(plan.K3sFrom), plan.K3sTo)
	} else {
		fmt.Fprintf(out, "  k3s     %s (current)\n", plan.K3sTo)
	}
	if role == layout.RoleAgent {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "This node is an agent: upgrade the servers first, one at a time, then each agent.")
		fmt.Fprintln(out)
		return
	}
	if bundleOwner != "" {
		fmt.Fprintf(out, "  bundle  maintained on %s\n", bundleOwner)
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
	if credentialMissing {
		detail := "minted during this upgrade"
		if !plan.K3sDrifted {
			detail += "; k3s restarts to load it (containers keep running)"
		}
		fmt.Fprintf(out, "  pull    node registry credential missing, %s\n", detail)
	}
	fmt.Fprintln(out)
}
