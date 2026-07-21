package limavm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Hinkolas/skali/internal/installer/host"
)

// ErrLaunchAgentLoad marks a LaunchAgent that was written but could not be
// loaded into the running session (typical over SSH, where no GUI launchd
// domain exists). Callers warn instead of failing: the agent still loads
// at the next login.
var ErrLaunchAgentLoad = errors.New("load launch agent")

// lookPath is a seam for tests; production resolves limactl on the PATH.
var lookPath = exec.LookPath

// LaunchAgentPath is where the login item lives for this user.
func LaunchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist"), nil
}

// renderLaunchAgent produces the plist that starts the VM at login.
// launchd jobs do not inherit the login PATH and limactl execs ssh, so the
// PATH is set explicitly to limactl's directory plus the system paths.
func renderLaunchAgent(limactlPath, instance, home string) string {
	logPath := filepath.Join(home, "Library", "Logs", "skali", "lima-launchagent.log")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + LaunchAgentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + limactlPath + `</string>
		<string>start</string>
		<string>` + instance + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>` + filepath.Dir(limactlPath) + `:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
	<key>StandardOutPath</key>
	<string>` + logPath + `</string>
	<key>StandardErrorPath</key>
	<string>` + logPath + `</string>
</dict>
</plist>
`
}

// InstallLaunchAgent writes the login item and loads it into the current
// GUI session. It returns the plist path even when loading fails with
// ErrLaunchAgentLoad, so callers can still name the file.
func InstallLaunchAgent(ctx context.Context, mac host.Runner, instance string) (string, error) {
	limactlPath, err := lookPath("limactl")
	if err != nil {
		return "", errors.New("limactl was not found on PATH; install Lima first: brew install lima")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
	if err := mac.MkdirAll(ctx, filepath.Dir(plistPath), 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(plistPath), err)
	}
	logsDir := filepath.Join(home, "Library", "Logs", "skali")
	if err := mac.MkdirAll(ctx, logsDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", logsDir, err)
	}
	plist := renderLaunchAgent(limactlPath, instance, home)
	if err := mac.WriteFile(ctx, plistPath, []byte(plist), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", plistPath, err)
	}

	domain := "gui/" + strconv.Itoa(os.Getuid())
	// A stale registration would make bootstrap fail; unloading first is
	// expected to fail when nothing is loaded, so the result is ignored.
	mac.Run(ctx, host.Command{
		Name: "launchctl",
		Args: []string{"bootout", domain + "/" + LaunchAgentLabel},
	})
	result, err := mac.Run(ctx, host.Command{
		Name: "launchctl",
		Args: []string{"bootstrap", domain, plistPath},
	})
	if err != nil {
		return plistPath, fmt.Errorf("%w: %v", ErrLaunchAgentLoad, err)
	}
	if result.ExitCode != 0 {
		return plistPath, fmt.Errorf("%w: exit %d: %s", ErrLaunchAgentLoad, result.ExitCode, tail(result.Stderr))
	}
	return plistPath, nil
}

// RemoveLaunchAgent unloads and deletes the login item; an absent agent is
// not an error.
func RemoveLaunchAgent(ctx context.Context, mac host.Runner) error {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	mac.Run(ctx, host.Command{
		Name: "launchctl",
		Args: []string{"bootout", domain + "/" + LaunchAgentLabel},
	})
	path, err := LaunchAgentPath()
	if err != nil {
		return err
	}
	return mac.Remove(ctx, path)
}

// AutoLoginUser reports the account macOS logs in automatically at boot,
// or an empty string when auto login is disabled. Without auto login a
// headless Mac never reaches the login session that starts the VM.
func AutoLoginUser(ctx context.Context, mac host.Runner) string {
	result, err := mac.Run(ctx, host.Command{
		Name: "defaults",
		Args: []string{"read", "/Library/Preferences/com.apple.loginwindow", "autoLoginUser"},
	})
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}
