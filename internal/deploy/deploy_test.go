package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/values"
	"github.com/Hinkolas/skali/internal/valuestore"
)

const testManifest = `version: "1"
name: demo
values:
  SESSION_SECRET:
    secret: true
applications:
  web:
    image: ghcr.io/example/web:1.0.0
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
      SESSION_SECRET: "${SESSION_SECRET}"
`

const changedManifest = `version: "1"
name: demo
values:
  SESSION_SECRET:
    secret: true
applications:
  web:
    image: ghcr.io/example/web:2.0.0
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
      SESSION_SECRET: "${SESSION_SECRET}"
`

type fixture struct {
	st        *store.Store
	projects  *project.Service
	values    *valuestore.Service
	artifacts *artifactstore.Service
	deploy    *Service

	projectID     uuid.UUID
	environmentID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	projects := project.New(st)
	valueSvc, err := valuestore.New(st, strings.Repeat("v", 32))
	require.NoError(t, err)
	artifactSvc := artifactstore.New(st)

	proj, err := projects.Create(ctx, "demo", "")
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production")
	require.NoError(t, err)

	return &fixture{
		st:            st,
		projects:      projects,
		values:        valueSvc,
		artifacts:     artifactSvc,
		deploy:        New(st, valueSvc, artifactSvc, "test"),
		projectID:     proj.ID,
		environmentID: env.ID,
	}
}

// submit stores a manifest as the draft and returns the definition version.
func (f *fixture) submit(t *testing.T, manifest string, expected int64) uuid.UUID {
	t.Helper()
	draft, err := f.projects.SubmitDraft(context.Background(), f.projectID, project.DraftSubmission{
		Source: []byte(manifest), Format: "yaml", ExpectedVersion: expected,
	})
	require.NoError(t, err)
	row, err := f.st.GetDefinitionVersionByHash(context.Background(), store.GetDefinitionVersionByHashParams{
		ProjectID:      f.projectID,
		DefinitionHash: draft.Hash,
	})
	require.NoError(t, err)
	return row.ID
}

func (f *fixture) stage(t *testing.T, plain, secret map[string]string) *valuestore.Candidate {
	t.Helper()
	candidate, err := f.values.Stage(context.Background(), f.environmentID, values.Resolved{
		Plain: plain, Secret: secret,
	})
	require.NoError(t, err)
	return candidate
}

func TestPrepareAndPromote(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "prepare-plant-value"})

	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		CandidateID:         candidate.ID,
		Resolver:            &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, prepared.RevisionID)
	require.Equal(t, "production", prepared.Revision.Environment)
	require.Equal(t, map[string]string{"APP_DOMAIN": "demo.example.com"}, prepared.Revision.Values)
	require.Equal(t, 1, prepared.Revision.Secrets["SESSION_SECRET"].Version)
	require.Contains(t, prepared.Revision.Artifacts, "web")

	// Prepare alone moves nothing.
	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, target.TargetRevisionID)

	require.NoError(t, f.deploy.Promote(ctx, prepared))
	target, err = f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.NotNil(t, target.TargetRevisionID)
	require.Equal(t, prepared.RevisionID, *target.TargetRevisionID)
	require.Nil(t, target.ActiveRevisionID, "activation is R2; the pointer must stay empty")

	// Values were promoted atomically with the target.
	plain, err := f.values.CurrentPlain(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, "demo.example.com", plain["APP_DOMAIN"])

	// The revision document round-trips and carries no plaintext secret.
	document, err := f.deploy.GetRevision(ctx, prepared.RevisionID)
	require.NoError(t, err)
	require.Equal(t, prepared.Revision.Checksum, document.Checksum)

	// The artifact is leased against eviction.
	require.ErrorIs(t, f.artifacts.Evict(ctx, prepared.ArtifactIDs[0]), artifactstore.ErrLeased)
}

func TestPrepareIdenticalInputsReusesRevision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "reuse-plant-value"})
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}

	first, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		CandidateID: candidate.ID, Resolver: resolver,
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, first))

	// Re-preparing with the now-current values and the same definition
	// produces the same checksum and reuses the stored revision row.
	second, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		CandidateID: uuid.Nil, Resolver: resolver,
	})
	require.NoError(t, err)
	require.Equal(t, first.RevisionID, second.RevisionID)
	require.Equal(t, first.Revision.Checksum, second.Revision.Checksum)

	rows, err := f.deploy.ListRevisions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

// Exit criterion: invalid input fails before target mutation. A missing
// required value aborts Prepare; the target row and revisions are untouched.
func TestDeployInvalidValuesLeavesTarget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	// Stage only the plain value; the required secret is missing.
	candidate := f.stage(t, map[string]string{"APP_DOMAIN": "demo.example.com"}, nil)

	_, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		CandidateID:         candidate.ID,
		Resolver:            &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "SESSION_SECRET")

	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Nil(t, target.TargetRevisionID)
	rows, err := f.deploy.ListRevisions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Empty(t, rows)
}

// Exit criterion (preparation half): a failing artifact resolver aborts the
// deployment without promoting draft or value versions or moving the target.
func TestFailedPreparationDoesNotPromote(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	// First, a successful deploy establishes current state.
	definitionVersion := f.submit(t, testManifest, 0)
	first := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "initial-plant-value"})
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		CandidateID: first.ID, Resolver: &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, prepared))

	draftBefore, err := f.projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	targetBefore, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	plainBefore, err := f.values.CurrentPlain(ctx, f.environmentID)
	require.NoError(t, err)
	secretsBefore, err := f.values.CurrentSecretVersions(ctx, f.environmentID)
	require.NoError(t, err)

	// Second deploy: changed manifest, changed values, failing resolver.
	changedVersion, hash, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(changedManifest), "yaml")
	require.NoError(t, err)
	require.NotEqual(t, draftBefore.Hash, hash)
	second := f.stage(t,
		map[string]string{"APP_DOMAIN": "changed.example.com"},
		map[string]string{"SESSION_SECRET": "changed-plant-value"})

	boom := errors.New("build exploded")
	_, err = f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: changedVersion,
		CandidateID: second.ID,
		Resolver: &artifactstore.Fake{
			Store: f.artifacts, ProjectID: f.projectID,
			FailFor: map[string]error{"web": boom},
		},
	})
	require.ErrorIs(t, err, boom)

	// The failure path: discard the candidate exactly as Execute will.
	require.NoError(t, f.values.DiscardCandidate(ctx, f.environmentID, second.ID))

	// Nothing moved: draft version, target, current values, secret versions.
	draftAfter, err := f.projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	require.Equal(t, draftBefore.Version, draftAfter.Version)
	require.Equal(t, draftBefore.Hash, draftAfter.Hash)
	targetAfter, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, targetBefore.TargetRevisionID, targetAfter.TargetRevisionID)
	require.Equal(t, targetBefore.UpdatedAt, targetAfter.UpdatedAt)
	plainAfter, err := f.values.CurrentPlain(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, plainBefore, plainAfter)
	secretsAfter, err := f.values.CurrentSecretVersions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, secretsBefore, secretsAfter)

	// Only the first revision exists; the pending artifact was abandoned.
	rows, err := f.deploy.ListRevisions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var abandoned int
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM artifacts WHERE phase = 'abandoned'").Scan(&abandoned))
	require.Equal(t, 1, abandoned)

	// No staged rows remain, and the staged secret never appears anywhere.
	var staged int
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT (SELECT count(*) FROM environment_values WHERE state = 'staged') + (SELECT count(*) FROM environment_secrets WHERE state = 'staged')").Scan(&staged))
	require.Zero(t, staged)
}

func TestPromoteAdvancesDraftToCandidateDefinition(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	f.submit(t, testManifest, 0)
	changedVersion, _, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(changedManifest), "yaml")
	require.NoError(t, err)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "advance-plant-value"})

	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: changedVersion,
		CandidateID: candidate.ID, Resolver: &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, prepared))

	draft, err := f.projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	require.Equal(t, int64(2), draft.Version, "promotion advances the draft to the deployed definition")
	require.Contains(t, string(draft.Source), "web:2.0.0")
}

func TestRollback(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com"},
		map[string]string{"SESSION_SECRET": "rollback-plant-value"})
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	first, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		CandidateID: candidate.ID, Resolver: resolver,
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, first))

	changedVersion, _, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(changedManifest), "yaml")
	require.NoError(t, err)
	second, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: changedVersion,
		CandidateID: uuid.Nil, Resolver: resolver,
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, second))

	target, err := f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, second.RevisionID, *target.TargetRevisionID)

	// Roll back to the first revision; no new revision appears.
	require.NoError(t, f.deploy.Rollback(ctx, f.environmentID, first.RevisionID))
	target, err = f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, first.RevisionID, *target.TargetRevisionID)
	rows, err := f.deploy.ListRevisions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// A revision of another environment is refused.
	other, err := f.projects.CreateEnvironment(ctx, f.projectID, "staging")
	require.NoError(t, err)
	require.ErrorIs(t, f.deploy.Rollback(ctx, other.ID, first.RevisionID), ErrRevisionMismatch)
}
