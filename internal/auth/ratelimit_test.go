package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRateLimiterWindow(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	l := newRateLimiter(func() time.Time { return now })

	for i := range 3 {
		require.True(t, l.allow("k", 3, time.Minute), "attempt %d", i+1)
	}
	require.False(t, l.allow("k", 3, time.Minute))

	// Other keys are independent.
	require.True(t, l.allow("other", 3, time.Minute))

	// A new window resets the counter.
	now = now.Add(time.Minute)
	require.True(t, l.allow("k", 3, time.Minute))
}

func TestRateLimiterPrunes(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	l := newRateLimiter(func() time.Time { return now })

	for i := range pruneThreshold {
		l.allow(string(rune(i))+"-key", 1, time.Minute)
	}
	require.GreaterOrEqual(t, len(l.entries), pruneThreshold)

	// All windows expire; the next insert triggers the sweep.
	now = now.Add(2 * time.Minute)
	l.allow("fresh", 1, time.Minute)
	require.Equal(t, 1, len(l.entries))
}
