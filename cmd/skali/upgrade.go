package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/clirender"
	"github.com/Hinkolas/skali/internal/installer"
	"github.com/Hinkolas/skali/internal/installer/host"
	"github.com/Hinkolas/skali/internal/updates"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

// newUpgradeCommand replaces this CLI binary with a published release. It
// is the in-binary form of install.sh: same channels, same pinned-version
// override, same checksum verification, same asset name. Only the CLI
// moves: skali-hostd is fetched by the cluster commands that install it,
// and a cluster moves with skali cluster upgrade.
func newUpgradeCommand() *cobra.Command {
	var requested, channelFlag string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update this skali CLI to a published release",
		Long: "Replaces this skali binary with a published release: the newest on a\n" +
			"channel (stable, or beta to include alpha, beta, and rc releases), or an\n" +
			"exact --version, which may also downgrade. Downloads are verified against\n" +
			"the release's checksums before anything is written.\n\n" +
			"The channel defaults to stable, or to beta when this build is itself a\n" +
			"prerelease. A cluster moves with skali cluster upgrade, not this command.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			opts, err := upgradeOptionsFromEnvironment(requested, channelFlag)
			if err != nil {
				return err
			}
			return runUpgrade(command.Context(), command.OutOrStdout(), opts)
		},
	}
	cmd.Flags().StringVar(&requested, "version", "", "exact release tag to install (overrides --channel; may downgrade)")
	cmd.Flags().StringVar(&channelFlag, "channel", "", "release channel, stable or beta (default stable; beta for a prerelease build)")
	return cmd
}

// upgradeOptions is everything runUpgrade needs, resolved once by the
// command so tests can hand it a fake feed, a fake release host, and a
// scratch executable.
type upgradeOptions struct {
	// Requested is the exact --version tag, or empty to follow Channel.
	Requested string
	Channel   updates.Channel
	// ChannelImplied marks a channel derived from the running build rather
	// than named on the command line.
	ChannelImplied bool
	// Current is the running version (version.Version).
	Current     string
	Feed        updates.Feed
	Client      *http.Client
	ReleaseBase string
	// Executable is the binary to replace, symlinks already resolved. It is
	// resolved once, before the replace: afterwards the running process's
	// own path reads as deleted on Linux.
	Executable   string
	Root         bool
	GOOS, GOARCH string
}

// upgradeOutcome is the judgement of one upgrade request: a target to
// install, or a message explaining why nothing moves.
type upgradeOutcome struct {
	Target    string
	Downgrade bool
	Message   string
}

func upgradeOptionsFromEnvironment(requested, channelFlag string) (upgradeOptions, error) {
	if requested != "" && channelFlag != "" {
		return upgradeOptions{}, errors.New("--version and --channel cannot be combined; --version names the exact release")
	}
	channel, implied, err := resolveUpgradeChannel(channelFlag, versionpkg.Version)
	if err != nil {
		return upgradeOptions{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return upgradeOptions{}, fmt.Errorf("locate the running skali binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return upgradeOptions{
		Requested:      requested,
		Channel:        channel,
		ChannelImplied: implied,
		Current:        versionpkg.Version,
		Feed: &updates.GitHubFeed{
			URL:       os.Getenv("SKALI_UPDATE_FEED_URL"),
			UserAgent: "skali/" + versionpkg.Version,
		},
		Client:      &http.Client{Timeout: 5 * time.Minute},
		ReleaseBase: releaseBase(),
		Executable:  executable,
		Root:        os.Geteuid() == 0,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
	}, nil
}

// resolveUpgradeChannel applies the default rule: an explicit flag wins;
// otherwise stable, except that a prerelease build follows beta, since a
// stable release may not exist yet while the user clearly opted into
// prereleases when installing.
func resolveUpgradeChannel(flag, current string) (channel updates.Channel, implied bool, err error) {
	if flag != "" {
		channel, err = updates.ParseChannel(flag)
		return channel, false, err
	}
	if versionpkg.IsPrerelease(current) {
		return updates.ChannelBeta, true, nil
	}
	return updates.ChannelStable, false, nil
}

// judgeUpgrade decides what, if anything, to install. latest is the feed's
// answer for the channel and is only consulted without an explicit request;
// nil means the channel has no release. Development builds have no
// comparable version and are only replaced on an explicit --version.
func judgeUpgrade(current, requested string, latest *updates.Release, channel updates.Channel) (upgradeOutcome, error) {
	if requested != "" {
		if !versionpkg.IsRelease(requested) {
			return upgradeOutcome{}, fmt.Errorf("--version must be an exact release tag, for example v0.1.0-alpha.1 (got %q)", requested)
		}
		if requested == current {
			return upgradeOutcome{Message: fmt.Sprintf("skali %s is already current", current)}, nil
		}
		downgrade := versionpkg.IsRelease(current) && versionpkg.Older(requested, current)
		return upgradeOutcome{Target: requested, Downgrade: downgrade}, nil
	}
	if !versionpkg.IsRelease(current) {
		return upgradeOutcome{}, fmt.Errorf("this is a development build (%s) and is not managed by skali upgrade; "+
			"pass --version to replace it with a release anyway", current)
	}
	if latest == nil {
		if channel == updates.ChannelStable {
			return upgradeOutcome{Message: "no release is published on the stable channel yet; try --channel beta"}, nil
		}
		return upgradeOutcome{Message: "no release is published yet"}, nil
	}
	if latest.Version == current {
		return upgradeOutcome{Message: fmt.Sprintf("skali %s is already current", current)}, nil
	}
	if versionpkg.Older(latest.Version, current) {
		message := fmt.Sprintf("skali %s is newer than the latest %s release %s; nothing to do",
			current, channel, latest.Version)
		if channel == updates.ChannelStable && versionpkg.IsPrerelease(current) {
			message += " (use --channel beta to follow prereleases)"
		}
		return upgradeOutcome{Message: message}, nil
	}
	return upgradeOutcome{Target: latest.Version}, nil
}

// runUpgrade resolves, downloads, verifies, installs, and checks. Nothing
// is written before every download has verified, and the CLI is restored
// when the new binary does not answer for its version. The writability of
// the install directory is checked first so an unprivileged run on a system
// install fails before any network round trip.
func runUpgrade(ctx context.Context, out io.Writer, opts upgradeOptions) error {
	style := clirender.StyleFor(out)
	directory := filepath.Dir(opts.Executable)
	if err := probeWritableDir(directory); err != nil {
		if errors.Is(err, fs.ErrPermission) && !opts.Root {
			return fmt.Errorf("installing to %s requires root; run sudo skali upgrade", directory)
		}
		return fmt.Errorf("cannot write to %s: %w", directory, err)
	}

	var latest *updates.Release
	if opts.Requested == "" && versionpkg.IsRelease(opts.Current) {
		var err error
		latest, err = opts.Feed.Latest(ctx, opts.Channel)
		if err != nil {
			return err
		}
	}
	outcome, err := judgeUpgrade(opts.Current, opts.Requested, latest, opts.Channel)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "skali upgrade\n  current  %s\n", opts.Current)
	if opts.Requested == "" {
		note := ""
		if opts.ChannelImplied {
			note = " (implied by prerelease build)"
		}
		fmt.Fprintf(out, "  channel  %s%s\n", opts.Channel, note)
	}
	if outcome.Target == "" {
		fmt.Fprintf(out, "\n%s\n", outcome.Message)
		return nil
	}
	target := outcome.Target
	label := ""
	if outcome.Downgrade {
		label = " (downgrade)"
	}
	fmt.Fprintf(out, "  target   %s%s\n\n", target, label)

	tasks := clirender.NewTasks(out)
	task := tasks.Start("Fetch checksums for " + target)
	sums, err := installer.ReleaseChecksums(ctx, opts.Client, opts.ReleaseBase, target)
	if err != nil {
		task.Fail()
		if errors.Is(err, installer.ErrAssetMissing) {
			return fmt.Errorf("release %s was not found", target)
		}
		return err
	}
	task.Done("")

	cliAsset := installer.CLIAsset(opts.GOOS, opts.GOARCH)
	task = tasks.Start("Download " + cliAsset)
	if sums[cliAsset] == "" {
		task.Fail()
		return fmt.Errorf("release %s publishes no %s", target, cliAsset)
	}
	binary, err := installer.DownloadAsset(ctx, opts.Client, opts.ReleaseBase, target, cliAsset, sums[cliAsset])
	if err != nil {
		task.Fail()
		return err
	}
	task.Done("checksum verified")

	task = tasks.Start("Install " + opts.Executable)
	previous, err := os.ReadFile(opts.Executable)
	if err != nil {
		task.Fail()
		return fmt.Errorf("read the current binary: %w", err)
	}
	if err := (host.Local{}).ReplaceFile(ctx, opts.Executable, "", binary, 0o755); err != nil {
		task.Fail()
		return fmt.Errorf("install %s: %w", opts.Executable, err)
	}
	task.Done("")

	task = tasks.Start("Verify skali --version reports " + target)
	if err := verifyInstalledCLI(ctx, opts.Executable, target); err != nil {
		task.Fail()
		if restoreErr := (host.Local{}).ReplaceFile(ctx, opts.Executable, "", previous, 0o755); restoreErr != nil {
			return fmt.Errorf("%w; restoring the previous binary failed too: %v", err, restoreErr)
		}
		return fmt.Errorf("%w; the previous binary was restored", err)
	}
	task.Done("")

	verb := "upgraded"
	if outcome.Downgrade {
		verb = "downgraded"
	}
	fmt.Fprintf(out, "\n%s%s skali %s -> %s\n", style.Check(), verb, opts.Current, target)
	return nil
}

// probeWritableDir reports whether this process can create and remove an
// entry in dir, which is what the rename-over replacement needs; the file
// itself may be owned by anyone.
func probeWritableDir(dir string) error {
	probe, err := os.CreateTemp(dir, ".skali-upgrade-")
	if err != nil {
		return err
	}
	name := probe.Name()
	probe.Close()
	return os.Remove(name)
}

// verifyInstalledCLI runs the freshly installed binary and checks it names
// the target version, proving both that it executes on this platform and
// that the release published the version it claims.
func verifyInstalledCLI(ctx context.Context, executable, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var output []byte
	err := retryTextFileBusy(ctx, func() error {
		var err error
		output, err = exec.CommandContext(ctx, executable, "--version").Output()
		return err
	})
	if err != nil {
		return fmt.Errorf("run %s --version: %w", executable, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 || fields[len(fields)-1] != want {
		return fmt.Errorf("the new binary reports %q, expected %s", strings.TrimSpace(string(output)), want)
	}
	return nil
}

// textFileBusyRetries bounds the wait for a just-written executable to
// become runnable.
const textFileBusyRetries = 20

// retryTextFileBusy runs attempt again while it fails with ETXTBSY. Linux
// refuses to execute a file another process holds open for writing, and a
// binary this process just wrote is exactly that for a moment: any child
// forked elsewhere in the process between the write and its own exec
// inherits the write descriptor until it execs (close-on-exec cannot help
// across that window). The condition clears within milliseconds; any other
// error returns at once.
func retryTextFileBusy(ctx context.Context, attempt func() error) error {
	for try := 0; ; try++ {
		err := attempt()
		if !errors.Is(err, syscall.ETXTBSY) || try >= textFileBusyRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(50 * time.Millisecond):
		}
	}
}
