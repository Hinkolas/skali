package artifact

import (
	"testing"

	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/stretchr/testify/require"
)

func TestPhaseMachine(t *testing.T) {
	t.Parallel()
	require.NoError(t, lifecycle.Verify(Phases))
	require.Equal(t, PhasePending, Phases.Initial())

	// Only verified artifacts can be evicted; an abandoned artifact never
	// had content to reclaim.
	require.True(t, Phases.Can(PhaseVerified, PhaseEvicted))
	require.False(t, Phases.Can(PhaseAbandoned, PhaseEvicted))

	// Verification is final: content cannot become unverified, and an
	// abandoned record is never revived. Re-importing creates a new record.
	require.False(t, Phases.Can(PhaseVerified, PhasePending))
	require.False(t, Phases.Can(PhaseAbandoned, PhaseVerified))
	require.False(t, Phases.Can(PhaseEvicted, PhaseVerified))

	// There is no failed phase; the build or import run carries the failure.
	require.False(t, Phases.Valid("failed"))

	require.True(t, Phases.Terminal(PhaseAbandoned))
	require.True(t, Phases.Terminal(PhaseEvicted))
	require.False(t, Phases.Terminal(PhasePending))
	require.False(t, Phases.Terminal(PhaseVerified))
}
