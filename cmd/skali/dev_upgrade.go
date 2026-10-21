package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliprompt"
	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/devremote"
	"github.com/Hinkolas/skali/internal/localdev"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func newDevUpgradeCommand(skalidImage *string) *cobra.Command {
	var yes bool
	command := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the local platform in place, keeping its data and development CA",
		Long: "Moves the existing local platform to this skali's release and k3s pin\n" +
			"without a reset: projects, environments, values, credentials, databases,\n" +
			"buckets, volumes, and the development CA (so its trust and every dev proxy\n" +
			"pinned to it) stay as they are. The plan (versions, component changes,\n" +
			"database migrations, and the expected interruption) is shown before\n" +
			"anything changes and needs confirmation or --yes. Releases only move\n" +
			"forward and k3s one minor version at a time; anything else is refused\n" +
			"before it starts. An interrupted or failed upgrade keeps all data;\n" +
			"running skali dev upgrade again continues it.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runDevUpgrade(command, *skalidImage, yes)
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "apply the plan without asking")
	return command
}

func runDevUpgrade(command *cobra.Command, skalidImage string, yes bool) error {
	if err := refuseWhileProxying(); err != nil {
		return err
	}
	// Ctrl-C ends the upgrade through its context, so the failure path
	// still reports where the platform stands.
	signalCtx, stopSignals := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()
	ctx, unlock, err := localdev.LockContext(signalCtx)
	if err != nil {
		return err
	}
	defer unlock()
	out := command.OutOrStdout()
	style := clirender.StyleFor(out)
	if err := localdev.ObsoletePlatforms(); err != nil {
		return err
	}
	target, err := upgradeSkalidImage(skalidImage)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, devVersionReason())
	plan, err := localdev.PlanUpgrade(ctx, target)
	if err != nil {
		return err
	}
	if plan.Current() {
		fmt.Fprintf(out, "%slocal platform %s already runs %s (%s); nothing to upgrade\n", style.Check(),
			localdev.ClusterName(), releaseName(plan.ToVersion), plan.ToK3sImage)
		return nil
	}
	printDevUpgradePlan(out, style, plan)
	if !yes {
		confirmed, err := cliprompt.New(os.Stdin, out).Confirm(ctx, cliprompt.ConfirmOptions{
			Title: "Upgrade the local platform?",
		})
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("aborted; the local platform is unchanged")
		}
	}

	progress := &taskProgress{tasks: clirender.NewTasks(out)}
	state, err := localdev.Upgrade(ctx, localdev.UpgradeOptions{SkalidImage: target, Progress: progress})
	if err != nil {
		progress.Abort()
		return upgradeFailure(err, signalCtx.Err() != nil)
	}
	if err := devSkewError(state.SkalidImage); err != nil {
		return err
	}
	if err := loginLocalRemote(ctx, state); err != nil {
		return err
	}
	ca, err := localdev.LoadCA()
	if err != nil {
		return err
	}
	if ca.Fingerprint != plan.CA.Fingerprint {
		return fmt.Errorf("the development CA changed during the upgrade (%s, was %s); this is a bug, please report it",
			devremote.FingerprintFromHex(ca.Fingerprint), devremote.FingerprintFromHex(plan.CA.Fingerprint))
	}
	fmt.Fprintf(out, "\n%slocal platform %s upgraded to %s (%s)\n", style.Check(),
		localdev.ClusterName(), releaseName(state.Version), state.K3sImage)
	fmt.Fprintf(out, "  %s  development CA %s unchanged; local and remote trust stay valid\n",
		style.Dim("kept"), devremote.FingerprintFromHex(ca.Fingerprint))
	return nil
}

// upgradeSkalidImage names the control-plane image an upgrade moves to: a
// released CLI's published image, or for a working-tree CLI the explicit
// --skalid-image, else the recorded one (a working-tree platform can only
// move its k3s pin; skali dev start rebuilds the image).
func upgradeSkalidImage(explicit string) (string, error) {
	if versionpkg.IsRelease(versionpkg.Version) {
		published := versionpkg.PublishedSkalidImage(versionpkg.Version)
		if explicit != "" && explicit != published {
			return "", fmt.Errorf("--skalid-image must be %s for this released CLI", published)
		}
		return published, nil
	}
	if platform, ok := versionpkg.PublishedSkalidVersion(explicit); ok {
		return "", fmt.Errorf("--skalid-image names released skalid %s; this working-tree CLI requires a development image", platform)
	}
	if explicit != "" {
		return explicit, nil
	}
	state, err := localdev.LoadState()
	if errors.Is(err, localdev.ErrNotInstalled) {
		return "", errors.New("no local platform is installed; skali dev creates one at this release")
	}
	if err != nil {
		return "", err
	}
	return state.SkalidImage, nil
}

// upgradeFailure adds where the platform stands to an upgrade error: still
// mid-upgrade (retry continues it) or rolled back untouched (the error
// says so already). An interrupt's own error is just the killed command.
func upgradeFailure(cause error, interrupted bool) error {
	state, err := localdev.LoadState()
	if err != nil || state.Upgrade == nil {
		return cause
	}
	if interrupted {
		cause = errors.New("interrupted")
	}
	return fmt.Errorf("%w\nthe upgrade to %s did not finish; the local platform's data and development CA are retained; "+
		"fix the cause above and run skali dev upgrade again to continue it (skali dev status shows where it stands)",
		cause, releaseName(state.Upgrade.ToVersion))
}

func releaseName(release string) string {
	if release == "" {
		return "working tree"
	}
	return release
}

// printDevUpgradePlan shows what the upgrade changes and what it costs before
// anything happens.
func printDevUpgradePlan(out io.Writer, style *clirender.Style, plan *localdev.UpgradePlan) {
	row := func(label, value string) {
		fmt.Fprintf(out, "  %s  %s\n", style.Dim(fmt.Sprintf("%-12s", label)), value)
	}
	more := func(value string) { fmt.Fprintf(out, "  %-12s  %s\n", "", value) }
	fmt.Fprintf(out, "\n%s\n", style.Bold("upgrade plan for local platform "+localdev.ClusterName()))
	if plan.Resuming != nil {
		row("resumes", "the upgrade started "+plan.Resuming.StartedAt.Local().Format("2006-01-02 15:04")+", which did not finish")
	}
	change := func(from, to string) string {
		if from == to {
			return to + " (unchanged)"
		}
		return from + " -> " + to
	}
	row("release", change(releaseName(plan.FromVersion), releaseName(plan.ToVersion)))
	row("skalid", change(plan.FromSkalidImage, plan.ToSkalidImage))
	row("k3s", change(plan.FromK3sImage, plan.ToK3sImage))
	for index, component := range plan.Components {
		value := component.Repository + " " + component.To + " (new)"
		if component.From != "" {
			value = component.Repository + " " + component.From + " -> " + component.To
		}
		if index == 0 {
			row("components", value)
		} else {
			more(value)
		}
	}
	switch {
	case plan.SchemaInstalled < 0:
		row("migrations", fmt.Sprintf("platform database up to schema %d, applied by skalid at start (current version unknown while stopped)", plan.SchemaTarget))
	case plan.SchemaInstalled < plan.SchemaTarget:
		row("migrations", fmt.Sprintf("platform database schema %d -> %d (%d forward-only, applied by skalid at start)",
			plan.SchemaInstalled, plan.SchemaTarget, plan.SchemaTarget-plan.SchemaInstalled))
	default:
		row("migrations", fmt.Sprintf("none (platform database at schema %d)", plan.SchemaInstalled))
	}
	row("keeps", "projects, environments, values, credentials, databases, buckets, volumes,")
	more("and the development CA " + devremote.FingerprintFromHex(plan.CA.Fingerprint) + " with its trust and dev proxies")
	var interruptions []string
	if plan.Status != localdev.ClusterRunning {
		interruptions = append(interruptions, "the stopped platform starts and stays running afterwards")
	}
	if plan.K3sChange() {
		interruptions = append(interruptions, "the node container is replaced on the new k3s; every application and database restarts (a few minutes)")
	}
	if plan.ReleaseChange() {
		interruptions = append(interruptions, "skalid restarts: the Studio, API, and skali commands against the local platform pause for about a minute")
	}
	if names := componentNames(plan.Components); len(names) > 0 {
		interruptions = append(interruptions, "restarts on new images: "+strings.Join(names, ", "))
	}
	for index, line := range interruptions {
		if index == 0 {
			row("interruption", line)
		} else {
			more(line)
		}
	}
	if plan.ReleaseChange() && plan.ToVersion != "" {
		row("afterwards", "skali releases older than "+plan.ToVersion+" can no longer run this platform")
	}
	fmt.Fprintln(out)
}

// componentNames names the moving components the way a developer knows
// them; postgres images under CNPG restart their databases.
func componentNames(components []localdev.ComponentChange) []string {
	var names []string
	add := func(name string) {
		for _, existing := range names {
			if existing == name {
				return
			}
		}
		names = append(names, name)
	}
	for _, component := range components {
		repository := component.Repository
		switch {
		case strings.HasSuffix(repository, "/cloudnative-pg"):
			add("CNPG (every database restarts once)")
		case strings.HasSuffix(repository, "/postgresql"):
			add("databases on that postgres image")
		case strings.Contains(repository, "cert-manager"):
			add("cert-manager")
		case strings.Contains(repository, "seaweedfs"):
			add("the object store")
		case strings.HasPrefix(repository, "registry"):
			add("the registry")
		case strings.Contains(repository, "traefik"):
			add("the Traefik edge")
		case strings.HasPrefix(repository, "rancher/"):
			add("k3s system components")
		default:
			add(repository)
		}
	}
	return names
}
