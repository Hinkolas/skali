package substrate

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/reconcile"
)

// A purge's Release reports each claim with what its teardown waits for,
// and a teardown pass pokes the environment only when that reason changes,
// so the purge's step refreshes per transition, not per pass.
func TestReleaseReportsTeardownWaitingReasons(t *testing.T) {
	fx := newSettleFixture(t)
	ctx := context.Background()
	fx.fake.set(clusterHealthyPhase, 1, true)
	_, phase := fx.pass(t)
	require.Equal(t, claim.PhaseProvisioned, phase)

	released, pending, err := fx.control.Release(ctx, fx.envID)
	require.NoError(t, err)
	require.False(t, released)
	require.Equal(t, []reconcile.ClaimRelease{{Service: "databases.data"}}, pending,
		"no teardown pass has run yet")

	// CNPG has not confirmed the drop: the pass waits and says so once.
	fx.fake.set(clusterHealthyPhase, 1, false)
	*fx.poked = nil
	requeue, phase := fx.pass(t)
	require.Equal(t, requeueWait, requeue)
	require.Equal(t, claim.PhaseReleasing, phase)
	reason := fx.control.WaitingReason(fx.claim.ID)
	require.True(t, strings.HasPrefix(reason, "dropping the database: "), reason)
	require.Equal(t, 1, countPokes(*fx.poked, fx), "a new reason pokes the environment")

	_, _ = fx.pass(t)
	require.Equal(t, 1, countPokes(*fx.poked, fx), "an unchanged reason does not")

	released, pending, err = fx.control.Release(ctx, fx.envID)
	require.NoError(t, err)
	require.False(t, released)
	require.Equal(t, []reconcile.ClaimRelease{{Service: "databases.data", Waiting: reason}}, pending)

	// The drop lands; the claim closes and Release reports completion.
	fx.fake.set(clusterHealthyPhase, 1, true)
	for range 5 {
		if _, phase = fx.pass(t); phase == claim.PhaseReleased {
			break
		}
	}
	require.Equal(t, claim.PhaseReleased, phase)
	require.Empty(t, fx.control.WaitingReason(fx.claim.ID))
	released, pending, err = fx.control.Release(ctx, fx.envID)
	require.NoError(t, err)
	require.True(t, released)
	require.Empty(t, pending)
}

func countPokes(poked []uuid.UUID, fx *settleFixture) int {
	count := 0
	for _, id := range poked {
		if id == fx.envID {
			count++
		}
	}
	return count
}
