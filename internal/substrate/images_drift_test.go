package substrate

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/localdev"
	"github.com/Hinkolas/skali/internal/substrate/cnpg"
)

// The localdev pre-pull list must always cover the default shared-pool
// image. localdev cannot import this package (it would drag the substrate
// into the CLI), so the agreement is pinned here instead.
func TestDefaultPoolImagePrePulled(t *testing.T) {
	t.Parallel()
	image, ok := cnpg.Lookup(DefaultEngine, DefaultMajor)
	require.True(t, ok)
	require.True(t, slices.Contains(localdev.RequiredImages(), image.Ref),
		"the default shared-pool image %s must be in localdev.RequiredImages", image.Ref)
}
