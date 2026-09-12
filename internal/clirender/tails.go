package clirender

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Hinkolas/skali/internal/client"
)

// Tail lines under a live step come in two shapes. The compact shape turns
// the server's structured checkpoints into a short status list: one row per
// TLS issuance attempt, one row per service for a health snapshot. The
// detail shape (skali --verbose) keeps every field the server recorded.
//
// Compact rows carry their own glyph colors; the tree renderer leaves
// pre-styled tail rows alone instead of dimming them.

// CertificateDetailLines keeps the full checkpoint detail available in CLI
// output and derives the countdown locally, without per-second journal
// writes.
func CertificateDetailLines(entry client.LogEntry, now time.Time) []string {
	lines := []string{}
	if retry, ok := fieldTime(entry.Fields, "next_retry_at"); ok {
		remaining := retry.Sub(now).Round(time.Second)
		if remaining > 0 {
			lines = append(lines, fmt.Sprintf("Next automatic retry in about %s (%s)", remaining, retry.Local().Format(time.RFC3339)))
		} else {
			lines = append(lines, "Automatic retry is due; waiting for cert-manager")
		}
	}
	return append(lines, entry.Message)
}

// CertificateLines summarizes a TLS checkpoint's snapshots, oldest first,
// as one row per issuance attempt: past attempts show why they failed, the
// current one what it waits for, and a valid certificate closes the list.
// A rollout-level failure and its guidance follow.
func CertificateLines(entries []client.LogEntry, now time.Time, style *Style) []string {
	if len(entries) == 0 {
		return nil
	}
	type attempt struct {
		number int
		latest client.LogEntry
	}
	var order []int
	byNumber := map[int]*attempt{}
	for _, entry := range entries {
		number := fieldInt(entry.Fields, "issuance_attempt")
		if current, ok := byNumber[number]; ok {
			current.latest = entry
			continue
		}
		byNumber[number] = &attempt{number: number, latest: entry}
		order = append(order, number)
	}
	sort.Ints(order)
	latest := entries[len(entries)-1]
	latestPhase := fieldString(latest.Fields, "phase")
	current := order[len(order)-1]

	var lines []string
	for _, number := range order {
		entry := byNumber[number].latest
		fields := entry.Fields
		phase := fieldString(fields, "phase")
		switch {
		case number == 0 && len(order) > 1:
			// The observation-only snapshot before any attempt was known.
			continue
		case phase == "active":
			text := "certificate is valid"
			if until, ok := fieldTime(fields, "valid_until"); ok {
				text += " until " + until.Local().Format("2006-01-02")
			}
			lines = append(lines, style.tailGlyph("ok")+" "+text)
		case phase == "issuing":
			lines = append(lines, style.tailGlyph("live")+" "+attemptLabel(number)+"  "+style.Dim(issuanceProgress(fields)))
		case phase == "backoff":
			lines = append(lines, style.tailGlyph("fail")+" "+attemptLabel(number)+" "+style.Red("failed")+"  "+style.Dim(issuanceFailure(fields)))
			if number == current {
				if retry, ok := fieldTime(fields, "next_retry_at"); ok {
					if remaining := retry.Sub(now).Round(time.Second); remaining > 0 {
						lines = append(lines, "  "+style.Dim(fmt.Sprintf("next attempt in about %s", remaining)))
					} else {
						lines = append(lines, "  "+style.Dim("next attempt is due; waiting for cert-manager"))
					}
				}
			}
		default:
			text := "waiting for the certificate to be observed"
			if number > 0 {
				text = attemptLabel(number) + "  " + style.Dim("waiting for issuance to start")
			}
			lines = append(lines, style.tailGlyph("wait")+" "+text)
		}
	}
	if failure := fieldString(latest.Fields, "failure"); failure != "" && latestPhase != "active" {
		lines = append(lines, style.tailGlyph("fail")+" "+style.Red(failure))
		if guidance := fieldString(latest.Fields, "guidance"); guidance != "" {
			lines = append(lines, "  "+style.Dim(guidance))
		}
	}
	for _, key := range []string{"recovery_error", "observation_error"} {
		if text := fieldString(latest.Fields, key); text != "" {
			lines = append(lines, style.tailGlyph("warn")+" "+style.Yellow(text))
		}
	}
	return lines
}

func attemptLabel(number int) string {
	return fmt.Sprintf("attempt %d", number)
}

// issuanceProgress names what the current attempt waits on, most specific
// first: the ACME challenge's own reason, then the order state, then the
// request condition, then the certificate message.
func issuanceProgress(fields map[string]any) string {
	if reason := challengeReason(fieldString(fields, "challenges")); reason != "" {
		return reason
	}
	if state := fieldString(fields, "order_state"); state != "" {
		text := "ACME order " + state
		if reason := fieldString(fields, "order_reason"); reason != "" {
			text += ": " + reason
		}
		return text
	}
	if status := fieldString(fields, "request_status"); status != "" {
		return status
	}
	if message := fieldString(fields, "message"); message != "" {
		return firstLine(message)
	}
	return "waiting for cert-manager"
}

// issuanceFailure is the one-line reason a past attempt failed.
func issuanceFailure(fields map[string]any) string {
	if message := fieldString(fields, "message"); message != "" {
		return firstLine(message)
	}
	if reason := challengeReason(fieldString(fields, "challenges")); reason != "" {
		return reason
	}
	if reason := fieldString(fields, "reason"); reason != "" {
		return reason
	}
	return "no reason recorded"
}

// challengeReason picks the reason line out of the inspected challenges:
// each challenge is "host (name): state; presented=.., processing=.."
// followed by its reason on the next line.
func challengeReason(challenges string) string {
	lines := strings.Split(challenges, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		text := strings.TrimSpace(lines[index])
		if text == "" || strings.Contains(text, "; presented=") {
			continue
		}
		return text
	}
	return ""
}

// HealthLines renders one health snapshot as a status list: a glyph per
// service health, the service key, its health, and the leading diagnostic.
func HealthLines(entry client.LogEntry, style *Style) []string {
	services, _ := entry.Fields["services"].([]any)
	if len(services) == 0 {
		return []string{entry.Message}
	}
	type row struct{ key, health, message string }
	rows := make([]row, 0, len(services))
	width := 0
	for _, item := range services {
		record, _ := item.(map[string]any)
		if record == nil {
			continue
		}
		key := fieldString(record, "key")
		rows = append(rows, row{key: key, health: fieldString(record, "health"), message: fieldString(record, "message")})
		width = max(width, len([]rune(key)))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	lines := make([]string, 0, len(rows))
	for _, item := range rows {
		line := style.tailGlyph(healthGlyphKind(item.health)) + " " + item.key + spaces(width-len([]rune(item.key))) + "  " + item.health
		if item.message != "" {
			line += "  " + style.Dim(item.message)
		}
		lines = append(lines, line)
	}
	return lines
}

func healthGlyphKind(health string) string {
	switch health {
	case "healthy":
		return "ok"
	case "progressing":
		return "live"
	case "degraded":
		return "warn"
	case "unhealthy":
		return "fail"
	}
	return "wait"
}

// tailGlyph is the single-cell status marker of a compact tail row. Plain
// output uses letters so the rows stay aligned and greppable.
func (s *Style) tailGlyph(kind string) string {
	if !s.on() {
		switch kind {
		case "ok":
			return "+"
		case "fail":
			return "x"
		case "warn":
			return "!"
		case "live":
			return ">"
		}
		return "."
	}
	switch kind {
	case "ok":
		return s.Green("✓")
	case "fail":
		return s.Red("✗")
	case "warn":
		return s.Yellow("!")
	case "live":
		return s.Cyan("›")
	}
	return s.Dim("○")
}

func fieldString(fields map[string]any, key string) string {
	switch value := fields[key].(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func fieldInt(fields map[string]any, key string) int {
	switch value := fields[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case string:
		var number int
		_, _ = fmt.Sscanf(value, "%d", &number)
		return number
	}
	return 0
}

func fieldTime(fields map[string]any, key string) (time.Time, bool) {
	raw, ok := fields[key].(string)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	return parsed, err == nil
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
