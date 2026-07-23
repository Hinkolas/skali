package installer

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/installer/host"
)

type installLog struct {
	runner host.Runner
	path   string
	body   bytes.Buffer
}

func newInstallLog(ctx context.Context, runner host.Runner, attemptID string) (*installLog, error) {
	if err := runner.MkdirAll(ctx, LogDir, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", LogDir, err)
	}
	suffix := attemptID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	log := &installLog{
		runner: runner,
		path: fmt.Sprintf("%s/install-%s-%s.log", LogDir,
			time.Now().UTC().Format("20060102-150405"), suffix),
	}
	log.line("install attempt %s", attemptID)
	if err := log.flush(ctx); err != nil {
		return nil, err
	}
	return log, nil
}

func (l *installLog) line(format string, args ...any) {
	fmt.Fprintf(&l.body, "%s ", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&l.body, format, args...)
	l.body.WriteByte('\n')
}

func (l *installLog) flush(ctx context.Context) error {
	if err := l.runner.WriteFile(ctx, l.path, l.body.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write install log %s: %w", l.path, err)
	}
	return nil
}

func collectK3sDiagnostics(ctx context.Context, runner host.Runner, role string) string {
	unit := "k3s.service"
	if role == "agent" {
		unit = "k3s-agent.service"
	}
	var sections []string
	for _, command := range []host.Command{
		{Name: "systemctl", Args: []string{"status", unit, "--no-pager", "--full"}},
		{Name: "journalctl", Args: []string{"-u", unit, "-b", "-n", "100", "--no-pager", "--full", "-o", "cat"}},
	} {
		result, err := runner.Run(ctx, command)
		if err != nil {
			sections = append(sections, command.Name+": "+err.Error())
			continue
		}
		output := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
		if output != "" {
			sections = append(sections, output)
		}
	}
	return strings.Join(sections, "\n\n")
}

func diagnosticCause(raw string) string {
	lines := strings.Split(raw, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		lower := strings.ToLower(line)
		if strings.Contains(lower, "level=fatal") || strings.Contains(lower, "error:") {
			return line
		}
	}
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}

func redactInstallText(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}
