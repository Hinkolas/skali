package clirender

import (
	"github.com/Hinkolas/skali/internal/client"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCertificateCountdown(t *testing.T) {
	now := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
	entry := client.LogEntry{Message: "Attempt 2 failed", Fields: map[string]any{"next_retry_at": now.Add(time.Minute).Format(time.RFC3339)}}
	require.Contains(t, CertificateLogLines(entry, now)[0], "in about 1m0s")
	require.Contains(t, CertificateLogLines(entry, now.Add(time.Minute))[0], "retry is due")
	delete(entry.Fields, "next_retry_at")
	require.Equal(t, []string{entry.Message}, CertificateLogLines(entry, now))
}
