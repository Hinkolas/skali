package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBytesSupportsDecimalAndBinaryUnits(t *testing.T) {
	t.Parallel()
	tests := map[string]int64{
		"1GB":    1_000_000_000,
		"1GiB":   1_073_741_824,
		"256MB":  256_000_000,
		"256MiB": 268_435_456,
		"1.5GB":  1_500_000_000,
	}
	for input, expected := range tests {
		actual, err := parseBytes(input)
		require.NoError(t, err, input)
		require.Equal(t, expected, actual, input)
	}
}

func TestParseCPUUsesExactMillicores(t *testing.T) {
	t.Parallel()
	actual, err := parseCPU("0.2")
	require.NoError(t, err)
	require.EqualValues(t, 200, actual)
	_, err = parseCPU("0.0005")
	require.ErrorContains(t, err, "three decimal places")
}

func TestParseDurationSupportsDaysAndWeeks(t *testing.T) {
	t.Parallel()
	day, err := parseDuration("1d")
	require.NoError(t, err)
	require.EqualValues(t, 86_400_000, day)
	week, err := parseDuration("1w")
	require.NoError(t, err)
	require.EqualValues(t, 604_800_000, week)
}
