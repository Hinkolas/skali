package installer

import (
	"context"
	"fmt"
	"strings"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// multipathBlacklistPath is the drop-in fragment that keeps multipathd off
// Longhorn's block devices; Debian-family multipath configurations include
// conf.d fragments by default.
const multipathBlacklistPath = "/etc/multipath/conf.d/skali-longhorn.conf"

// multipathBlacklist excludes plain SCSI/virtio disk nodes from multipath
// claiming. Without it multipathd grabs a Longhorn volume the moment it
// attaches and the filesystem mount fails with "device or resource busy"
// (the known Longhorn multipath conflict).
const multipathBlacklist = `# Managed by skali: keep multipathd off Longhorn volume devices.
blacklist {
    devnode "^sd[a-z0-9]+"
}
`

// EnsureStoragePrerequisites installs and enables the Longhorn host
// dependencies on a Debian-family host: open-iscsi with iscsid active, and
// a multipath blacklist when multipathd is present. It is idempotent and
// cheap when everything is already in place, so install, upgrade, and
// repair all run it unconditionally.
func EnsureStoragePrerequisites(ctx context.Context, runner host.Runner, progress Progress) error {
	if progress == nil {
		progress = silentProgress{}
	}
	progress.Start("Install storage prerequisites")
	status := probeStoragePrerequisites(ctx, runner)
	changed := false

	if !status.iscsidPresent {
		progress.Note("installing open-iscsi")
		if err := aptInstall(ctx, runner, "open-iscsi"); err != nil {
			return err
		}
		changed = true
	}
	if !status.iscsidActive {
		// The Debian package ships iscsid socket-activated; volumes attach
		// more predictably with the daemon running outright.
		result, err := runner.Run(ctx, host.Command{Name: "systemctl", Args: []string{"enable", "--now", "iscsid"}})
		if err != nil {
			return fmt.Errorf("enable iscsid: %w", err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("enable iscsid: exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
		}
		changed = true
	}

	if status.multipathdPresent && !status.multipathBlacklisted {
		if err := runner.MkdirAll(ctx, "/etc/multipath/conf.d", 0o755); err != nil {
			return fmt.Errorf("create multipath conf.d: %w", err)
		}
		if err := runner.WriteFile(ctx, multipathBlacklistPath, []byte(multipathBlacklist), 0o644); err != nil {
			return fmt.Errorf("write multipath blacklist: %w", err)
		}
		if status.multipathdActive {
			result, err := runner.Run(ctx, host.Command{Name: "systemctl", Args: []string{"restart", "multipathd"}})
			if err != nil {
				return fmt.Errorf("restart multipathd: %w", err)
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("restart multipathd: exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
			}
		}
		changed = true
	}

	if !changed {
		progress.Skip("already installed")
		return nil
	}
	detail := "open-iscsi"
	if status.multipathdPresent {
		detail += ", multipath blacklist"
	}
	progress.Done(detail)
	return nil
}

type storagePrereqStatus struct {
	iscsidPresent        bool
	iscsidActive         bool
	multipathdPresent    bool
	multipathdActive     bool
	multipathBlacklisted bool
}

// probeStoragePrerequisites reads the host state Longhorn depends on,
// without mutating anything; diagnose shares it with the ensure path.
func probeStoragePrerequisites(ctx context.Context, runner host.Runner) storagePrereqStatus {
	status := storagePrereqStatus{}
	iscsid := probeUnit(ctx, runner, "iscsid")
	status.iscsidPresent = iscsid.present
	status.iscsidActive = iscsid.active
	multipathd := probeUnit(ctx, runner, "multipathd")
	status.multipathdPresent = multipathd.present
	status.multipathdActive = multipathd.active
	if info, err := runner.Stat(ctx, multipathBlacklistPath); err == nil && info.Exists {
		status.multipathBlacklisted = true
	}
	return status
}

// aptInstall installs one package non-interactively. Supported install
// targets are Debian-family hosts (see installK3sFiles), so apt is the
// only package manager the installer speaks.
func aptInstall(ctx context.Context, runner host.Runner, pkg string) error {
	env := []string{"DEBIAN_FRONTEND=noninteractive"}
	update, err := runner.Run(ctx, host.Command{Name: "apt-get", Args: []string{"update", "-qq"}, Env: env})
	if err != nil {
		return fmt.Errorf("apt-get update: %w", err)
	}
	if update.ExitCode != 0 {
		return fmt.Errorf("apt-get update: exit %d: %s", update.ExitCode, strings.TrimSpace(update.Stderr))
	}
	install, err := runner.Run(ctx, host.Command{Name: "apt-get", Args: []string{"install", "-y", pkg}, Env: env})
	if err != nil {
		return fmt.Errorf("apt-get install %s: %w", pkg, err)
	}
	if install.ExitCode != 0 {
		return fmt.Errorf("apt-get install %s: exit %d: %s", pkg, install.ExitCode, strings.TrimSpace(install.Stderr))
	}
	return nil
}
