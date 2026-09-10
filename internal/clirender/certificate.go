package clirender

import (
	"fmt"
	"time"

	"github.com/Hinkolas/skali/internal/client"
)

// CertificateLogLines keeps the full checkpoint detail available in CLI
// output and derives the countdown locally, without per-second journal writes.
func CertificateLogLines(entry client.LogEntry, now time.Time) []string {
	lines := []string{}
	if raw, ok := entry.Fields["next_retry_at"].(string); ok {
		if retry, err := time.Parse(time.RFC3339, raw); err == nil {
			remaining := retry.Sub(now).Round(time.Second)
			if remaining > 0 {
				lines = append(lines, fmt.Sprintf("Next automatic retry in about %s (%s)", remaining, retry.Local().Format(time.RFC3339)))
			} else {
				lines = append(lines, "Automatic retry is due; waiting for cert-manager")
			}
		}
	}
	return append(lines, entry.Message)
}
