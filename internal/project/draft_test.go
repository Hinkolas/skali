package project

import (
	"context"
	"testing"

	"github.com/Hinkolas/skali/internal/yamldoc"
	"github.com/stretchr/testify/require"
)

const validManifest = `version: "1"
name: demo
applications:
  api:
    image: ghcr.io/example/api:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
`

const updatedManifest = `version: "1"
name: demo
applications:
  api:
    image: ghcr.io/example/api:1.1.0
    ports:
      http:
        port: 8080
        protocol: http
`

func TestSubmitDraftAndGet(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	proj, err := svc.Create(ctx, "demo", "")
	require.NoError(t, err)

	_, err = svc.GetDraft(ctx, proj.ID)
	require.ErrorIs(t, err, ErrDraftNotFound)

	draft, err := svc.SubmitDraft(ctx, proj.ID, DraftSubmission{
		Source: []byte(validManifest), Format: "yaml", ExpectedVersion: 0,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), draft.Version)
	require.NotEmpty(t, draft.Hash)

	// A stale or zero expected version must be rejected, never merged.
	_, err = svc.SubmitDraft(ctx, proj.ID, DraftSubmission{
		Source: []byte(updatedManifest), Format: "yaml", ExpectedVersion: 0,
	})
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = svc.SubmitDraft(ctx, proj.ID, DraftSubmission{
		Source: []byte(updatedManifest), Format: "yaml", ExpectedVersion: 7,
	})
	require.ErrorIs(t, err, ErrVersionConflict)

	next, err := svc.SubmitDraft(ctx, proj.ID, DraftSubmission{
		Source: []byte(updatedManifest), Format: "yaml", ExpectedVersion: 1,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), next.Version)
	require.NotEqual(t, draft.Hash, next.Hash)

	got, err := svc.GetDraft(ctx, proj.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), got.Version)
	require.Equal(t, "yaml", got.Format)
	require.Equal(t, []byte(updatedManifest), got.Source)
	require.Equal(t, next.Hash, got.Hash)
	require.Equal(t, "demo", got.Definition.Name)
	require.Contains(t, got.Definition.Applications, "api")
}

// Exit criterion: invalid or unknown fields fail before any mutation. The
// draft version and the stored definition versions must be untouched.
func TestSubmitDraftInvalidWritesNothing(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	proj, err := svc.Create(ctx, "demo", "")
	require.NoError(t, err)
	_, err = svc.SubmitDraft(ctx, proj.ID, DraftSubmission{
		Source: []byte(validManifest), Format: "yaml", ExpectedVersion: 0,
	})
	require.NoError(t, err)

	invalid := validManifest + "unknownfield: true\n"
	_, err = svc.SubmitDraft(ctx, proj.ID, DraftSubmission{
		Source: []byte(invalid), Format: "yaml", ExpectedVersion: 1,
	})
	require.Error(t, err)
	var diagnostics yamldoc.Diagnostics
	require.ErrorAs(t, err, &diagnostics)

	count, err := svc.st.CountDefinitionVersions(ctx, proj.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	draft, err := svc.GetDraft(ctx, proj.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), draft.Version)
}

func TestSubmitDraftNameMismatch(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	proj, err := svc.Create(ctx, "other", "")
	require.NoError(t, err)
	_, err = svc.SubmitDraft(ctx, proj.ID, DraftSubmission{
		Source: []byte(validManifest), Format: "yaml", ExpectedVersion: 0,
	})
	require.ErrorIs(t, err, ErrNameMismatch)
}

func TestSubmitCandidateDoesNotMoveDraft(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	proj, err := svc.Create(ctx, "demo", "")
	require.NoError(t, err)
	_, err = svc.SubmitDraft(ctx, proj.ID, DraftSubmission{
		Source: []byte(validManifest), Format: "yaml", ExpectedVersion: 0,
	})
	require.NoError(t, err)

	id, hash, err := svc.SubmitCandidate(ctx, proj.ID, []byte(updatedManifest), "yaml")
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	count, err := svc.st.CountDefinitionVersions(ctx, proj.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	draft, err := svc.GetDraft(ctx, proj.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), draft.Version)
	require.NotEqual(t, hash, draft.Hash)

	// Identical content reuses the stored version.
	again, hashAgain, err := svc.SubmitCandidate(ctx, proj.ID, []byte(updatedManifest), "yaml")
	require.NoError(t, err)
	require.Equal(t, id, again)
	require.Equal(t, hash, hashAgain)
	count, err = svc.st.CountDefinitionVersions(ctx, proj.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
}
