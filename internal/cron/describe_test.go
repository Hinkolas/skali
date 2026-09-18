package cron

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDescribe(t *testing.T) {
	cases := map[string]string{
		"0 3 * * *":    "daily at 03:00",
		"30 14 * * *":  "daily at 14:30",
		"15 * * * *":   "hourly at :15",
		"0 */6 * * *":  "every 6 hours",
		"*/15 * * * *": "every 15 minutes",
		"* * * * *":    "every minute",
		"0 4 * * 0":    "weekly on Sunday at 04:00",
		"0 4 * * 7":    "weekly on Sunday at 04:00",
		"0 2 1 * *":    "monthly on the 1st at 02:00",
		"0 2 22 * *":   "monthly on the 22nd at 02:00",
		"0 3 * 6 *":    "0 3 * 6 *",
		"0 3 1,15 * *": "0 3 1,15 * *",
		"nonsense":     "nonsense",
	}
	for expr, want := range cases {
		require.Equal(t, want, Describe(expr), expr)
	}
}
