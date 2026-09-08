package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/localdev"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// runDevUpgrade moves the local platform's control plane to this CLI's
// skalid: the working-tree build inside the repository, otherwise the
// published image of a released CLI. Everything after target selection is
// the ordinary forced Ensure (import, pod roll, bundle converge), so
// upgrade repairs as it goes, exactly like skali dev start --force. The k3s pin is
// the one thing that cannot move in place (the k3d node container is the
// image), so its drift is reported with the reset path instead.
func runDevUpgrade(command *cobra.Command, skalidImage string) error {
	ctx := command.Context()
	out := command.OutOrStdout()
	style := clirender.StyleFor(out)

	state, err := localdev.LoadState()
	if errors.Is(err, localdev.ErrNotInstalled) {
		return errors.New("the local platform is not installed; run skali dev start first")
	}
	if err != nil {
		return err
	}

	target, repoRoot := skalidImage, ""
	explicit := target != ""
	if !explicit {
		target, repoRoot, err = upgradeTarget()
		if err != nil {
			return err
		}
	}
	// Published tags are immutable, so equality is identity; a working-tree
	// or explicit target can always carry new bits under an old name and is
	// never "current". Downgrades are refused: schema migrations only move
	// forward, so an older skalid over a newer database is a broken
	// platform, not a rollback.
	current, currentOK := versionpkg.PublishedSkalidVersion(state.SkalidImage)
	targetVersion, targetOK := versionpkg.PublishedSkalidVersion(target)
	if !explicit && currentOK && targetOK && versionpkg.Older(targetVersion, current) {
		return fmt.Errorf("the local platform runs skalid %s, newer than this CLI's %s: "+
			"upgrade the CLI, or skali dev reset for a fresh platform at this CLI's version",
			current, targetVersion)
	}
	skalidCurrent := currentOK && targetOK && current == targetVersion
	k3sDrifted := state.K3sImage != localdev.K3sImage
	if skalidCurrent && !k3sDrifted {
		fmt.Fprintln(out, "already current, nothing to do")
		return nil
	}

	fmt.Fprintf(out, "upgrade plan for the local platform (cluster %s)\n", localdev.ClusterName())
	if skalidCurrent {
		fmt.Fprintf(out, "  skalid  %s (current)\n", target)
	} else if state.SkalidImage == target {
		fmt.Fprintf(out, "  skalid  %s (reimport and roll)\n", target)
	} else {
		fmt.Fprintf(out, "  skalid  %s -> %s\n", state.SkalidImage, target)
	}
	if k3sDrifted {
		fmt.Fprintf(out, "  k3s     %s -> %s (recreation only; see below)\n", state.K3sImage, localdev.K3sImage)
	} else {
		fmt.Fprintf(out, "  k3s     %s (current)\n", localdev.K3sImage)
	}
	fmt.Fprintln(out)
	if k3sDrifted {
		fmt.Fprintln(out, style.Yellow("the k3s pin cannot move on the existing k3d node; to pick it up,"))
		fmt.Fprintln(out, style.Yellow("skali dev reset (destroys local data) and the next skali dev"))
		fmt.Fprintln(out, style.Yellow("recreates the platform current"))
		fmt.Fprintln(out)
	}
	if skalidCurrent {
		return nil
	}

	if err := materializeSkalidImage(ctx, out, target, repoRoot); err != nil {
		return err
	}
	if _, err := ensureLocalPlatform(command, target, true); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%supgrade complete: skalid %s\n", style.Check(), target)
	return nil
}

// upgradeTarget names this CLI's skalid without materializing it. The
// working tree wins so a developer's upgrade tracks their checkout, the
// same precedence a fresh install uses; a dev build outside the repository
// has no image of its own to offer.
func upgradeTarget() (image, repoRoot string, err error) {
	if root := findRepoRoot(); root != "" {
		return "skalid:dev", root, nil
	}
	if versionpkg.IsRelease(versionpkg.Version) {
		return versionpkg.PublishedSkalidImage(versionpkg.Version), "", nil
	}
	return "", "", fmt.Errorf("this CLI is a dev build (%s) outside the skali repository, "+
		"so no skalid image can be derived from it; pass --skalid-image", versionpkg.Version)
}

// materializeSkalidImage makes the target exist in the local docker
// daemon, where Ensure's import picks it up: a working-tree build, or a
// pull that is free when the published image is already cached.
func materializeSkalidImage(ctx context.Context, out io.Writer, target, repoRoot string) error {
	tasks := clirender.NewTasks(out)
	if repoRoot != "" {
		task := tasks.Start("Build " + target + " from the working tree")
		if err := localdev.BuildSkalidImage(ctx, repoRoot, target, task.NoteWriter()); err != nil {
			task.Fail()
			return err
		}
		task.Done("")
		return nil
	}
	task := tasks.Start("Pull " + target)
	if err := localdev.EnsureHostImage(ctx, target); err != nil {
		task.Fail()
		return err
	}
	task.Done("")
	return nil
}

// upgradeHint names a local platform trailing a released CLI; empty
// otherwise. Dev builds, working-tree platforms, and custom images have no
// comparable versions, and a platform ahead of the CLI is the CLI's
// problem, not the platform's.
func upgradeHint(recorded string) string {
	if !versionpkg.IsRelease(versionpkg.Version) {
		return ""
	}
	current, ok := versionpkg.PublishedSkalidVersion(recorded)
	if !ok || !versionpkg.Older(current, versionpkg.Version) {
		return ""
	}
	return fmt.Sprintf("the local platform runs skalid %s and this CLI is %s; skali dev upgrade moves it",
		current, versionpkg.Version)
}
