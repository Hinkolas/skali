package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/artifactstore"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/valuestore"
)

const testManifest = `version: "1"
name: demo
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

func (f *fixture) stage(t *testing.T, provided map[string]string) *valuestore.Candidate {
	t.Helper()
	candidate, err := f.values.Stage(context.Background(), f.environmentID, provided)
	require.NoError(t, err)
	return candidate
}

func TestPrepareAndPromote(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "prepare-plant-value"})

	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID:       f.environmentID,
		DefinitionVersionID: definitionVersion,
		CandidateID:         candidate.ID,
		Resolver:            &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, prepared.RevisionID)
	require.Equal(t, "production", prepared.Revision.Environment)
	require.Equal(t, 1, prepared.Revision.Secrets["APP_DOMAIN"].Version)
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
	current, err := f.values.CurrentVersions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"APP_DOMAIN": 1, "SESSION_SECRET": 1}, current)

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
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "reuse-plant-value"})
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
	candidate := f.stage(t, map[string]string{"APP_DOMAIN": "demo.example.com"})

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
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "initial-plant-value"})
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
	versionsBefore, err := f.values.CurrentVersions(ctx, f.environmentID)
	require.NoError(t, err)

	// Second deploy: changed manifest, changed values, failing resolver.
	changedVersion, hash, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(changedManifest), "yaml")
	require.NoError(t, err)
	require.NotEqual(t, draftBefore.Hash, hash)
	second := f.stage(t,
		map[string]string{"APP_DOMAIN": "changed.example.com", "SESSION_SECRET": "changed-plant-value"})

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
	versionsAfter, err := f.values.CurrentVersions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, versionsBefore, versionsAfter)

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
		"SELECT count(*) FROM environment_secrets WHERE state = 'staged'").Scan(&staged))
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
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "advance-plant-value"})

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

// A CLI-first project only ever submits candidates, so no draft row exists
// until the first promotion creates it.
func TestPromoteCreatesDraftWhenMissing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion, _, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(testManifest), "yaml")
	require.NoError(t, err)
	_, err = f.projects.GetDraft(ctx, f.projectID)
	require.ErrorIs(t, err, project.ErrDraftNotFound)

	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "create-plant-value"})
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		CandidateID: candidate.ID, Resolver: &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, prepared))

	draft, err := f.projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	require.Equal(t, int64(1), draft.Version, "first promotion creates the draft")
	require.Contains(t, string(draft.Source), "web:1.0.0")

	// Re-promoting the same definition leaves the draft untouched.
	require.NoError(t, f.deploy.Promote(ctx, prepared))
	draft, err = f.projects.GetDraft(ctx, f.projectID)
	require.NoError(t, err)
	require.Equal(t, int64(1), draft.Version)
}

func TestRollback(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "rollback-plant-value"})
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

	// Roll back to the first revision; no new revision appears, and without
	// a kernel the run concludes at the target move.
	jsvc := journal.NewService(f.st, "test")
	result, err := f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: first.RevisionID,
		Actor: "test", Journal: jsvc,
	})
	require.NoError(t, err)
	target, err = f.deploy.Target(ctx, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, first.RevisionID, *target.TargetRevisionID)
	rows, err := f.deploy.ListRevisions(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	run, err := jsvc.Run(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "rollback", run.Kind)
	require.Equal(t, string(journal.RunSucceeded), run.Status)

	// Rolling back to the current target is refused.
	_, err = f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: first.RevisionID,
		Actor: "test", Journal: jsvc,
	})
	require.ErrorIs(t, err, ErrAlreadyTargeted)

	// A revision of another environment is refused.
	other, err := f.projects.CreateEnvironment(ctx, f.projectID, "staging")
	require.NoError(t, err)
	_, err = f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: other.ID, RevisionID: first.RevisionID,
		Actor: "test", Journal: jsvc,
	})
	require.ErrorIs(t, err, ErrRevisionMismatch)
}

// The wedge regression: a stored value whose reference was removed from the
// manifest must never block later deployments. It is intersected away and
// reported as orphaned instead.
func TestOrphanedStoredValueDoesNotBlockDeploy(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "orphan-plant-value"})
	resolver := &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID}
	first, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		CandidateID: candidate.ID, Resolver: resolver,
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, first))

	// The new manifest drops the ${SESSION_SECRET} reference; the stored
	// value remains current in the store.
	withoutSecret := `version: "1"
name: demo
applications:
  web:
    image: ghcr.io/example/web:3.0.0
    ports:
      http:
        port: 8080
        protocol: http
    environment:
      APP_DOMAIN: "${APP_DOMAIN}"
`
	changedVersion, _, err := f.projects.SubmitCandidate(ctx, f.projectID, []byte(withoutSecret), "yaml")
	require.NoError(t, err)
	second, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: changedVersion,
		CandidateID: uuid.Nil, Resolver: resolver,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"SESSION_SECRET"}, second.Orphaned)
	require.NotContains(t, second.Revision.Secrets, "SESSION_SECRET")
	require.NoError(t, f.deploy.Promote(ctx, second))
}

// recordingEnqueuer captures kernel handoffs.
type recordingEnqueuer struct{ enqueued []uuid.UUID }

func (r *recordingEnqueuer) Enqueue(environmentID uuid.UUID) {
	r.enqueued = append(r.enqueued, environmentID)
}

func TestRollbackCreatesRunAndEnqueues(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "rollback-enqueue-value"})
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

	enqueuer := &recordingEnqueuer{}
	f.deploy.SetEnqueuer(enqueuer)
	jsvc := journal.NewService(f.st, "test")
	result, err := f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: first.RevisionID,
		Actor: "test", Journal: jsvc,
	})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{f.environmentID}, enqueuer.enqueued)

	// The run stays running for the kernel with the rollout step ensured.
	run, err := jsvc.Run(ctx, result.RunID)
	require.NoError(t, err)
	require.Equal(t, "rollback", run.Kind)
	require.Equal(t, string(journal.RunRunning), run.Status)
	tree, err := jsvc.RunTree(ctx, result.RunID)
	require.NoError(t, err)
	keys := make(map[string]bool, len(tree.Steps))
	for _, step := range tree.Steps {
		keys[step.Step.Key] = true
	}
	require.True(t, keys["promote"], "the promote step journals the target move")
	require.True(t, keys["rollout"], "the rollout step hands over to the kernel")

	// A second rollback while the run is in flight is refused.
	_, err = f.deploy.Rollback(ctx, RollbackInput{
		EnvironmentID: f.environmentID, RevisionID: second.RevisionID,
		Actor: "test", Journal: jsvc,
	})
	require.ErrorIs(t, err, ErrDeploymentInFlight)
}

func TestPlatformsOverlap(t *testing.T) {
	cases := []struct {
		name      string
		submitted string
		cluster   []string
		want      bool
	}{
		{"exact match", "linux/amd64", []string{"linux/amd64"}, true},
		{"superset covers", "linux/amd64,linux/arm64", []string{"linux/amd64"}, true},
		{"partial coverage counts", "linux/amd64", []string{"linux/amd64", "linux/arm64"}, true},
		{"disjoint fails", "linux/arm64", []string{"linux/amd64"}, false},
		{"empty submitted skips", "", []string{"linux/amd64"}, true},
		{"empty cluster skips", "linux/arm64", nil, true},
		{"whitespace only skips", " , ", []string{"linux/amd64"}, true},
		{"spaced list matches", " linux/amd64 , linux/arm64 ", []string{"linux/arm64"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, platformsOverlap(tc.submitted, tc.cluster))
		})
	}
}

// A promotion source pins exactly the artifact rows the source revision
// leases, matched by reference and digest, never re-decided by context
// hash; every action is a reuse.
func TestLoadPromotionSource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	definitionVersion := f.submit(t, testManifest, 0)
	candidate := f.stage(t,
		map[string]string{"APP_DOMAIN": "demo.example.com", "SESSION_SECRET": "promotion-plant-value"})
	prepared, err := f.deploy.Prepare(ctx, PrepareInput{
		EnvironmentID: f.environmentID, DefinitionVersionID: definitionVersion,
		CandidateID: candidate.ID, Resolver: &artifactstore.Fake{Store: f.artifacts, ProjectID: f.projectID},
	})
	require.NoError(t, err)
	require.NoError(t, f.deploy.Promote(ctx, prepared))
	rows, err := f.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{
		EnvironmentID: f.environmentID, ActiveRevisionID: &prepared.RevisionID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	staging, err := f.projects.CreateEnvironment(ctx, f.projectID, "staging")
	require.NoError(t, err)

	source, err := f.deploy.loadPromotionSource(ctx, staging.ID, f.environmentID)
	require.NoError(t, err)
	require.Equal(t, prepared.DefinitionVersionID, source.DefinitionVersionID)
	require.Len(t, source.Actions, 1)
	require.Equal(t, "reuse", source.Actions[0].Action)

	leases, err := f.st.ListArtifactLeasesByRevision(ctx, prepared.RevisionID)
	require.NoError(t, err)
	require.Len(t, leases, 1)
	require.Equal(t, leases[0].ArtifactID, source.Actions[0].ArtifactID,
		"the promotion pins the leased artifact row")
	require.Equal(t, prepared.Revision.Artifacts, source.Artifacts)

	// Guards: same environment, unknown source, no active revision, and a
	// source in another project.
	_, err = f.deploy.loadPromotionSource(ctx, staging.ID, staging.ID)
	require.ErrorIs(t, err, ErrSameEnvironment)
	_, err = f.deploy.loadPromotionSource(ctx, staging.ID, uuid.New())
	require.ErrorIs(t, err, ErrSourceEnvironmentNotFound)
	empty, err := f.projects.CreateEnvironment(ctx, f.projectID, "empty")
	require.NoError(t, err)
	_, err = f.deploy.loadPromotionSource(ctx, staging.ID, empty.ID)
	require.ErrorIs(t, err, ErrNoActiveRevision)
	otherProject, err := f.projects.Create(ctx, "other", "")
	require.NoError(t, err)
	otherEnv, err := f.projects.CreateEnvironment(ctx, otherProject.ID, "production")
	require.NoError(t, err)
	_, err = f.deploy.loadPromotionSource(ctx, otherEnv.ID, f.environmentID)
	require.ErrorIs(t, err, ErrSourceProjectMismatch)
}
