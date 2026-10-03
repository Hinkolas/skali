package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
)

func TestReleaseWaitImage(t *testing.T) {
	withRelease := compiler.ProjectDefinition{Applications: map[string]compiler.Application{
		"web": {Deployment: compiler.Deployment{ReleaseCommand: compiler.ReleaseCommand{Command: []string{"/bin/migrate"}}}},
	}}
	withoutRelease := compiler.ProjectDefinition{Applications: map[string]compiler.Application{"web": {}}}

	lookups := 0
	k := &Kernel{deps: Deps{WaitImage: func(context.Context) (string, error) {
		lookups++
		return "registry.invalid/skalid:1", nil
	}}}
	require.Equal(t, "registry.invalid/skalid:1", k.releaseWaitImage(context.Background(), withRelease))
	// Definitions without release commands never touch the lookup.
	require.Empty(t, k.releaseWaitImage(context.Background(), withoutRelease))
	require.Equal(t, 1, lookups)

	// A failed lookup renders without the wait instead of failing the pass.
	k.deps.WaitImage = func(context.Context) (string, error) { return "", errors.New("no deployment") }
	require.Empty(t, k.releaseWaitImage(context.Background(), withRelease))

	k.deps.WaitImage = nil
	require.Empty(t, k.releaseWaitImage(context.Background(), withRelease))
}
