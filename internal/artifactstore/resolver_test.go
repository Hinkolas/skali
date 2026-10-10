package artifactstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
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
	source := compiler.ApplicationSource{Kind: "build"}

	// Only verified records resolve.
	resolver := &RecordResolver{Records: map[string]*store.Artifact{"web": record}}
	_, err = resolver.Resolve(ctx, "web", source)
	require.ErrorContains(t, err, "not verified")

	require.NoError(t, svc.Verify(ctx, record.ID, "localhost:5510/skali/demo/web", testDigest, nil))
	verified, err := svc.Get(ctx, record.ID)
	require.NoError(t, err)
	resolver.Records["web"] = verified
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
