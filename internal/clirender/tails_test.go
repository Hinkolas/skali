package clirender

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/client"
)

func tlsEntry(seq int64, phase string, attempt int, extra map[string]any) client.LogEntry {
	fields := map[string]any{"tls": true, "phase": phase, "certificate": "tls-web-public"}
	if attempt > 0 {
		fields["issuance_attempt"] = float64(attempt) // JSON numbers decode as float64
	}
	for key, value := range extra {
		fields[key] = value
	}
	return client.LogEntry{Seq: seq, Message: "snapshot " + phase, Fields: fields}
}

func TestCertificateLinesTimeline(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 11, 11, 30, 0, 0, time.UTC)
	challenge := "[redacted:APP_DOMAIN] (tls-web-public-2294053783): pending; presented=true, processing=true\n" +
		"Waiting for HTTP-01 challenge propagation: wrong status code '503', expected '200'"
	entries := []client.LogEntry{
		tlsEntry(1, "pending", 0, nil),
		tlsEntry(2, "issuing", 1, map[string]any{"challenges": challenge}),
		tlsEntry(3, "backoff", 1, map[string]any{
			"message":       "The certificate request has failed to complete and will be retried: order is in \"errored\" state\nsecond line",
			"next_retry_at": now.Add(90 * time.Second).Format(time.RFC3339),
		}),
	}
	lines := CertificateLines(entries, now, nil)
	require.Equal(t, []string{
		"x attempt 1 failed  The certificate request has failed to complete and will be retried: order is in \"errored\" state",
		"  next attempt in about 1m30s",
	}, lines)

	entries = append(entries, tlsEntry(4, "issuing", 2, map[string]any{"challenges": challenge}))
	lines = CertificateLines(entries, now, nil)
	require.Equal(t, []string{
		"x attempt 1 failed  The certificate request has failed to complete and will be retried: order is in \"errored\" state",
		"> attempt 2  Waiting for HTTP-01 challenge propagation: wrong status code '503', expected '200'",
	}, lines, "the retry countdown belongs to the current attempt only")

	entries = append(entries, tlsEntry(5, "active", 2, map[string]any{"valid_until": "2026-12-10T11:30:00Z"}))
	lines = CertificateLines(entries, now, nil)
	require.Len(t, lines, 2)
	require.True(t, strings.HasPrefix(lines[1], "+ certificate is valid until 2026-12-"), lines[1])

	// Without challenge detail the order, then the request, then the
	// certificate message describe the wait.
	require.Equal(t, "ACME order pending", issuanceProgress(map[string]any{"order_state": "pending"}))
	require.Equal(t, "Approved=True: approved", issuanceProgress(map[string]any{"request_status": "Approved=True: approved"}))
	require.Equal(t, "waiting for cert-manager", issuanceProgress(map[string]any{}))
}

func TestCertificateLinesFailureAndErrors(t *testing.T) {
	t.Parallel()
	now := time.Now()
	entries := []client.LogEntry{
		tlsEntry(1, "backoff", 2, map[string]any{
			"message":           "order errored",
			"failure":           "Next automatic retry is after the rollout deadline",
			"guidance":          "Check public A/AAAA DNS and HTTP port 80 reachability, then redeploy.",
			"observation_error": "certificate was replaced; waiting for observation",
		}),
	}
	lines := CertificateLines(entries, now, nil)
	require.Equal(t, []string{
		"x attempt 2 failed  order errored",
		"x Next automatic retry is after the rollout deadline",
		"  Check public A/AAAA DNS and HTTP port 80 reachability, then redeploy.",
		"! certificate was replaced; waiting for observation",
	}, lines)

	// Before any attempt is known the row says what is awaited.
	require.Equal(t, []string{". waiting for the certificate to be observed"},
		CertificateLines([]client.LogEntry{tlsEntry(1, "pending", 0, nil)}, now, nil))
	require.Nil(t, CertificateLines(nil, now, nil))
}

func TestCertificateLinesStyled(t *testing.T) {
	t.Parallel()
	style := &Style{Enabled: true}
	lines := CertificateLines([]client.LogEntry{tlsEntry(1, "issuing", 1, map[string]any{"order_state": "pending"})}, time.Now(), style)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], "\x1b[36m›\x1b[0m attempt 1")
}

func TestCertificateDetailLines(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
	entry := client.LogEntry{Message: "Attempt 2 failed\nreason: Failed", Fields: map[string]any{"next_retry_at": now.Add(time.Minute).Format(time.RFC3339)}}
	require.Contains(t, CertificateDetailLines(entry, now)[0], "in about 1m0s")
	require.Contains(t, CertificateDetailLines(entry, now.Add(time.Minute))[0], "retry is due")
	delete(entry.Fields, "next_retry_at")
	require.Equal(t, []string{entry.Message}, CertificateDetailLines(entry, now))
}

func TestHealthLines(t *testing.T) {
	t.Parallel()
	entry := client.LogEntry{Message: "data: healthy\nweb: progressing (waiting for placement)", Fields: map[string]any{
		"health": true,
		"services": []any{
			map[string]any{"key": "web", "type": "application", "health": "progressing", "code": "rollout-pending", "message": "waiting for placement and provisioning"},
			map[string]any{"key": "data", "type": "database", "health": "healthy"},
			map[string]any{"key": "assets", "type": "bucket", "health": "unhealthy", "message": "store unavailable"},
		},
	}}
	require.Equal(t, []string{
		"x assets  unhealthy  store unavailable",
		"+ data    healthy",
		"> web     progressing  waiting for placement and provisioning",
	}, HealthLines(entry, nil))

	// An entry without the structured list falls back to its message.
	require.Equal(t, []string{"plain"}, HealthLines(client.LogEntry{Message: "plain"}, nil))
}

func TestCompactTailRowsKeepTheirColors(t *testing.T) {
	t.Parallel()
	style := &Style{Enabled: true}
	tree := &client.RunTree{Run: client.Run{ID: "1", Kind: "deployment"},
		Steps: []client.Step{{ID: "verify", Title: "Verify health", Status: "waiting"}}}
	lines := treeLines(tree, func(string) []string {
		return []string{style.Green("✓") + " data  healthy", "plain detail"}
	}, &treeView{style: style})
	require.Len(t, lines, 4)
	require.Equal(t, "      "+style.Green("✓")+" data  healthy", lines[2], "a styled row is not dimmed again")
	require.Equal(t, "      "+style.Dim("plain detail"), lines[3])
}
