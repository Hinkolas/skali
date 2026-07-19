package claim

import (
	"testing"

	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/stretchr/testify/require"
)

func TestPhaseMachine(t *testing.T) {
	t.Parallel()
	require.NoError(t, lifecycle.Verify(Phases))
	require.Equal(t, PhasePending, Phases.Initial())

	// A claim deleted before it was ever placed releases directly.
	require.True(t, Phases.Can(PhasePending, PhaseReleased))
	// A revoked placement returns an unprovisioned claim to pending.
	require.True(t, Phases.Can(PhaseBound, PhasePending))
	// Partial provisioning may exist, so a bound claim tears down through
	// releasing rather than releasing directly.
	require.True(t, Phases.Can(PhaseBound, PhaseReleasing))
	require.False(t, Phases.Can(PhaseBound, PhaseReleased))

	// Relocation keeps the phase; a provisioned claim never regresses.
	require.False(t, Phases.Can(PhaseProvisioned, PhaseBound))
	require.False(t, Phases.Can(PhaseProvisioned, PhasePending))

	// Teardown is one-way: data may already be destroyed.
	require.False(t, Phases.Can(PhaseReleasing, PhaseProvisioned))
	require.False(t, Phases.Can(PhaseReleasing, PhaseBound))

	// There is no failed phase; failure detail lives in runs and diagnostics.
	require.False(t, Phases.Valid("failed"))

	require.True(t, Phases.Terminal(PhaseReleased))
	for _, phase := range []Phase{PhasePending, PhaseBound, PhaseProvisioned, PhaseReleasing} {
		require.False(t, Phases.Terminal(phase), phase)
	}
}
