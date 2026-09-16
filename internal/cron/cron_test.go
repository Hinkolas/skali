package cron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func at(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func TestParseRejectsBadExpressions(t *testing.T) {
	cases := map[string]string{
		"":             "expected 5 fields",
		"* * * *":      "expected 5 fields (minute hour day-of-month month day-of-week), got 4",
		"* * * * * *":  "expected 5 fields (minute hour day-of-month month day-of-week), got 6",
		"60 * * * *":   "field 1 (minute): 60 is out of range 0-59",
		"* 24 * * *":   "field 2 (hour): 24 is out of range 0-23",
		"* * 32 * *":   "field 3 (day of month): 32 is out of range 1-31",
		"* * 0 * *":    "field 3 (day of month): 0 is out of range 1-31",
		"* * * 13 *":   "field 4 (month): 13 is out of range 1-12",
		"* * * * 8":    "field 5 (day of week): 8 is out of range 0-7",
		"* * * * x":    "field 5 (day of week): \"x\" is not a number or a name",
		"a * * * *":    "field 1 (minute): \"a\" is not a number",
		"*/0 * * * *":  "field 1 (minute): step \"0\" must be a positive number",
		"10-5 * * * *": "field 1 (minute): range \"10-5\" runs backwards",
		"1,,2 * * * *": "field 1 (minute): has an empty list element",
		"0 0 31 4 *":   "never fires",
		"0 0 30 feb *": "never fires",
		"@daily":       "expected 5 fields (minute hour day-of-month month day-of-week), got 1",
		"0 0 * * ?":    "field 5 (day of week): \"?\" is not a number or a name",
	}
	for expr, want := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := Parse(expr)
			require.Error(t, err)
			require.Contains(t, err.Error(), want)
		})
	}
}

func TestParseNormalizes(t *testing.T) {
	s, err := Parse("  0   3 * *   * ")
	require.NoError(t, err)
	require.Equal(t, "0 3 * * *", s.String())
}

func TestNext(t *testing.T) {
	cases := []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{"every minute", "* * * * *", at(2026, 9, 16, 10, 0), at(2026, 9, 16, 10, 1)},
		{"strictly after", "0 3 * * *", at(2026, 9, 16, 3, 0), at(2026, 9, 17, 3, 0)},
		{"daily crosses midnight", "0 3 * * *", at(2026, 9, 16, 4, 30), at(2026, 9, 17, 3, 0)},
		{"seconds are truncated", "0 3 * * *", at(2026, 9, 16, 2, 59).Add(30 * time.Second), at(2026, 9, 16, 3, 0)},
		{"every fifteen minutes", "*/15 * * * *", at(2026, 9, 16, 10, 16), at(2026, 9, 16, 10, 30)},
		{"stepped range", "0 9-17/4 * * *", at(2026, 9, 16, 9, 0), at(2026, 9, 16, 13, 0)},
		{"list", "0 6,18 * * *", at(2026, 9, 16, 6, 0), at(2026, 9, 16, 18, 0)},
		{"month rollover", "0 0 1 * *", at(2026, 9, 16, 0, 0), at(2026, 10, 1, 0, 0)},
		{"year rollover", "0 0 1 1 *", at(2026, 9, 16, 0, 0), at(2027, 1, 1, 0, 0)},
		{"leap day", "0 0 29 2 *", at(2026, 1, 1, 0, 0), at(2028, 2, 29, 0, 0)},
		{"month name", "0 12 1 mar *", at(2026, 9, 16, 0, 0), at(2027, 3, 1, 12, 0)},
		{"weekly by name", "30 7 * * mon", at(2026, 9, 16, 0, 0), at(2026, 9, 21, 7, 30)},
		{"sunday as 7", "0 0 * * 7", at(2026, 9, 16, 0, 0), at(2026, 9, 20, 0, 0)},
		{"dom or dow when both restricted", "0 0 15 * fri", at(2026, 9, 16, 0, 0), at(2026, 9, 18, 0, 0)},
		{"dom or dow picks the earlier", "0 0 17 * mon", at(2026, 9, 16, 0, 0), at(2026, 9, 17, 0, 0)},
		{"non-utc input answers in utc", "0 3 * * *",
			time.Date(2026, 9, 16, 4, 0, 0, 0, time.FixedZone("plus2", 2*3600)), at(2026, 9, 16, 3, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Parse(tc.expr)
			require.NoError(t, err)
			got := s.Next(tc.after)
			require.True(t, tc.want.Equal(got), "want %s, got %s", tc.want, got)
			require.Equal(t, time.UTC, got.Location())
		})
	}
}

func TestNextIsMonotonic(t *testing.T) {
	s, err := Parse("*/7 2,14 * * 1-5")
	require.NoError(t, err)
	previous := at(2026, 1, 1, 0, 0)
	for range 500 {
		next := s.Next(previous)
		require.False(t, next.IsZero())
		require.True(t, next.After(previous))
		require.Contains(t, []int{2, 14}, next.Hour())
		require.Zero(t, next.Minute()%7)
		require.NotEqual(t, time.Saturday, next.Weekday())
		require.NotEqual(t, time.Sunday, next.Weekday())
		previous = next
	}
}
