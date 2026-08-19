package layout

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPriorityClassFor(t *testing.T) {
	t.Parallel()
	require.Equal(t, "skali-high", PriorityClassFor("high"))
	require.Equal(t, "skali-normal", PriorityClassFor("normal"))
	require.Equal(t, "skali-normal", PriorityClassFor(""))
	// The names are API once pods reference them; pin them with their order.
	require.Equal(t, "skali-critical", PriorityClassCritical)
	require.Greater(t, PriorityClassCriticalValue, PriorityClassHighValue)
	require.Greater(t, PriorityClassHighValue, PriorityClassNormalValue)
	require.Equal(t, 0, PriorityClassNormalValue)
}
