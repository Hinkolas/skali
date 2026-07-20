package artifactstore

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/revision"
)

func TestRecordResolver(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	ctx := context.Background()

	record, err := svc.CreatePending(ctx, Pending{
		Application: "web",
		Kind:        revision.KindBuildLocal,
		ContextHash: "input-hash",
	})
	require.NoError(t, err)
	resolver := &RecordResolver{Store: svc, IDs: map[string]uuid.UUID{"web": record.ID}}
	source := compiler.ApplicationSource{Kind: "build"}

	// Only verified records resolve.
	_, err = resolver.Resolve(ctx, "web", source)
	require.ErrorContains(t, err, "not verified")

	require.NoError(t, svc.Verify(ctx, record.ID, "localhost:5510/skali/demo/web", testDigest, nil))
	resolved, err := resolver.Resolve(ctx, "web", source)
	require.NoError(t, err)
	require.Equal(t, record.ID, resolved.ArtifactID)
	require.Equal(t, "localhost:5510/skali/demo/web", resolved.Artifact.Reference)
	require.Equal(t, testDigest, resolved.Artifact.Digest)
	require.Equal(t, revision.KindBuildLocal, resolved.Artifact.Kind)
	require.Equal(t, "input-hash", resolved.Artifact.ContextHash)

	// Applications without a recorded artifact fail loudly.
	_, err = resolver.Resolve(ctx, "worker", source)
	require.ErrorContains(t, err, "no artifact recorded")
}
