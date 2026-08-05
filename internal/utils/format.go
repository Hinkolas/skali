package utils

import (
	"fmt"
	"time"
)

// FormatBytes renders a byte count for humans without dropping to zero
// for small values.
func FormatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// HumanSince renders how long ago t was, falling back to an absolute
// local timestamp beyond a day.
func HumanSince(t time.Time) string {
	elapsed := time.Since(t).Round(time.Second)
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}

// PluralCount renders a count with a naively pluralized noun
// ("1 server", "2 servers").
func PluralCount(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
