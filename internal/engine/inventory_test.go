package engine_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/engine"
	"github.com/Hinkolas/skali/internal/engine/enginetest"
)

func TestInventorySamplerReadySemantics(t *testing.T) {
	ctx := context.Background()
	fake := enginetest.New()
	fake.Inv = engine.Inventory{
		Images: []engine.Image{
			{ID: "sha256:aaa", RepoTags: []string{"small:latest"}, SizeBytes: 10},
			{ID: "sha256:bbb", RepoTags: []string{"big:latest"}, SizeBytes: 100, Containers: 2},
		},
		Volumes: []engine.Volume{
			{Name: "zeta", Driver: "local"},
			{Name: "alpha", Driver: "local", Containers: 1},
		},
	}
	s := engine.NewInventorySampler(fake)

	// Cold: unknown, never "empty node".
	_, ok := s.Latest()
	require.False(t, ok, "cold sampler must report unknown")

	// One successful sample is enough (no rates, no two-sample rule), and the
	// result is sorted: images largest first, volumes by name.
	s.SampleNow(ctx)
	inv, ok := s.Latest()
	require.True(t, ok)
	require.Equal(t, []string{"sha256:bbb", "sha256:aaa"}, []string{inv.Images[0].ID, inv.Images[1].ID})
	require.Equal(t, []string{"alpha", "zeta"}, []string{inv.Volumes[0].Name, inv.Volumes[1].Name})
	require.Equal(t, 2, inv.Images[0].Containers)

	// Engine unreachable: unknown beats stale.
	fake.InventoryErr = context.DeadlineExceeded
	s.SampleNow(ctx)
	_, ok = s.Latest()
	require.False(t, ok, "unreachable engine must flip back to unknown")

	// Recovery.
	fake.InventoryErr = nil
	s.SampleNow(ctx)
	inv, ok = s.Latest()
	require.True(t, ok)
	require.Len(t, inv.Images, 2)
	require.Len(t, inv.Volumes, 2)
}
