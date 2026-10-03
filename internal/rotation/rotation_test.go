package rotation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/claim"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/dbstore"
	"github.com/Hinkolas/skali/internal/deploy"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/module"
	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/reconcile"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/substrate"
	"github.com/Hinkolas/skali/internal/substrate/seaweed"
	"github.com/Hinkolas/skali/internal/testdb"
)

// fakeRotator stands in for the substrate: it records the commit and, when
// commit is set, plays the claim worker's part by committing the key to
// the row right away.
type fakeRotator struct {
	mu     sync.Mutex
	db     *dbstore.Service
	calls  []Input
	commit bool
	err    error
}

func (f *fakeRotator) RotateBucketCredentials(ctx context.Context, environmentID uuid.UUID, serviceKey string, retireAfter time.Duration) (substrate.BucketRotation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Input{EnvironmentID: environmentID, ServiceKey: serviceKey, RetireAfter: retireAfter})
	if f.err != nil {
		return substrate.BucketRotation{}, f.err
	}
	retireAt := time.Now().Add(retireAfter).UTC().Truncate(time.Second)
	if f.commit {
		claimRow, err := f.db.LiveServiceBucketClaim(ctx, environmentID, serviceKey)
		if err != nil {
			return substrate.BucketRotation{}, err
		}
		allocation, err := f.db.LiveAllocation(ctx, claimRow.ID)
		if err != nil {
			return substrate.BucketRotation{}, err
		}
		if _, err := f.db.BeginAllocationCredentialRotation(ctx, allocation.ID, "AKNEW", retireAt); err != nil {
			return substrate.BucketRotation{}, err
		}
	}
	return substrate.BucketRotation{AccessKey: "AKNEW", RetireAt: retireAt}, nil
}

// fakeStatus answers the environment projection: the first `before`
// reads return the pre-roll projection, every later one the rolled one.
type fakeStatus struct {
	mu     sync.Mutex
	before int
	calls  int
	old    *reconcile.Status
	rolled *reconcile.Status
}

func (f *fakeStatus) status(context.Context, uuid.UUID) (*reconcile.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.before || f.rolled == nil {
		return f.old, nil
	}
	return f.rolled, nil
}

type fakeRevisions struct{ rev *revision.Revision }

func (f fakeRevisions) GetRevision(context.Context, uuid.UUID) (*revision.Revision, error) {
	return f.rev, nil
}

type fixture struct {
	st        *store.Store
	db        *dbstore.Service
	journal   *journal.Service
	rotator   *fakeRotator
	status    *fakeStatus
	control   *Controller
	projectID uuid.UUID
	envID     uuid.UUID
	claim     *store.BucketClaim
	alloc     *store.BucketAllocation
}

func output(service, output string) compiler.Expression {
	return compiler.Expression{Parts: []compiler.ExpressionPart{{
		Kind: "service_output", Collection: "buckets", Service: service, Output: output}}}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	dbSvc := dbstore.New(st)
	projects := project.New(st)
	journalSvc := journal.NewService(st, uuid.NewString())

	suffix := uuid.Must(uuid.NewV7()).String()[24:]
	proj, err := projects.Create(ctx, "demo"+suffix, "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)

	owner := dbstore.ServiceOwner(proj.ID, env.ID, proj.Name, "production", "files")
	claimRow, err := dbSvc.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)
	sw, err := dbSvc.CreateObjectStore(ctx, dbstore.StoreInput{
		Name: seaweed.StoreName, Masters: 1, VolumeServers: 1,
		Replication: "000", VolumeStorageBytes: 1 << 30, Image: seaweed.Image,
	})
	require.NoError(t, err)
	allocation, err := dbSvc.RecordAllocation(ctx, dbstore.AllocationInput{
		ClaimID: claimRow.ID, StoreID: sw.ID, BucketName: "b-files-01",
		AccessKeyID: "AKOLD", CredentialSecret: "s3cred-x",
		Endpoint: substrate.InternalBucketEndpoint(), Region: seaweed.Region,
	})
	require.NoError(t, err)
	_, err = dbSvc.TransitionBucketClaim(ctx, claimRow.ID, claim.PhaseProvisioned)
	require.NoError(t, err)

	target := &reconcile.RevisionRef{ID: uuid.Must(uuid.NewV7())}
	old := time.Now().Add(-time.Hour)
	fresh := module.SourceStatus{State: module.SourceFresh}
	status := &fakeStatus{
		before: 2,
		old: &reconcile.Status{State: deploy.EnvironmentStateActive, Target: target, Observation: fresh,
			Services: []reconcile.ServiceStatus{
				{Key: "web", Type: "application", Health: module.HealthHealthy,
					Pods: []reconcile.PodInfo{{Name: "web-blue-1", Started: old, Serving: true}}},
				{Key: "other", Type: "application", Health: module.HealthHealthy,
					Pods: []reconcile.PodInfo{{Name: "other-1", Started: old, Serving: true}}},
			}},
		rolled: &reconcile.Status{State: deploy.EnvironmentStateActive, Target: target, Observation: fresh,
			Services: []reconcile.ServiceStatus{
				{Key: "web", Type: "application", Health: module.HealthHealthy,
					Pods: []reconcile.PodInfo{{Name: "web-green-1", Started: time.Now().Add(time.Minute), Serving: true}}},
				{Key: "other", Type: "application", Health: module.HealthHealthy,
					Pods: []reconcile.PodInfo{{Name: "other-1", Started: old, Serving: true}}},
			}},
	}
	rev := &revision.Revision{Definition: compiler.ProjectDefinition{Applications: map[string]compiler.Application{
		"web":   {Environment: map[string]compiler.Expression{"S3_SECRET_KEY": output("files", "secret_key")}},
		"other": {Environment: map[string]compiler.Expression{"UPLOADS": output("uploads", "name")}},
	}}}
	rotator := &fakeRotator{db: dbSvc, commit: true}
	control := New(Deps{
		Store: st, Journal: journalSvc, DB: dbSvc,
		Revisions: fakeRevisions{rev: rev}, Buckets: rotator, Status: status.status,
	}, Config{CommitTimeout: 300 * time.Millisecond, RollTimeout: 300 * time.Millisecond, PollInterval: 10 * time.Millisecond})
	return &fixture{
		st: st, db: dbSvc, journal: journalSvc, rotator: rotator, status: status, control: control,
		projectID: proj.ID, envID: env.ID, claim: claimRow, alloc: allocation,
	}
}

func (fx *fixture) steps(t *testing.T, runID uuid.UUID) map[string]string {
	t.Helper()
	tree, err := fx.journal.RunTree(context.Background(), runID)
	require.NoError(t, err)
	flat := map[string]string{}
	for _, step := range tree.Steps {
		flat[step.Step.Key] = step.Step.Status
	}
	return flat
}

func (fx *fixture) run(t *testing.T, runID uuid.UUID) store.Run {
	t.Helper()
	run, err := fx.st.GetRunByID(context.Background(), runID)
	require.NoError(t, err)
	return run
}

func (fx *fixture) stepMessages(t *testing.T, runID uuid.UUID, key string) []string {
	t.Helper()
	ctx := context.Background()
	step, ok, err := fx.journal.FindStep(ctx, runID, key)
	require.NoError(t, err)
	require.True(t, ok)
	events, err := fx.journal.StepLogs(ctx, step.ID, journal.Cursor{}, 100)
	require.NoError(t, err)
	messages := make([]string, 0, len(events))
	for _, event := range events {
		messages = append(messages, event.Message)
	}
	return messages
}

// The happy path: the commit lands, the worker takes it, the consumer
// rolls onto pods started after the bump, the run succeeds with both steps
// and the version in its narration. The non-consumer's old pod is nobody's
// business.
func TestRotationRollsConsumers(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()

	runID, err := fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "files", RetireAfter: time.Hour, Actor: "alice"})
	require.NoError(t, err)
	require.Equal(t, "running", fx.run(t, runID).Status)
	require.NoError(t, fx.control.process(ctx, runID))

	run := fx.run(t, runID)
	require.Equal(t, "succeeded", run.Status)
	require.Equal(t, Kind, run.Kind)
	require.Equal(t, "alice", run.Actor)
	require.Equal(t, map[string]string{"rotate": "succeeded", "roll": "succeeded"}, fx.steps(t, runID))
	require.Equal(t, []Input{{EnvironmentID: fx.envID, ServiceKey: "files", RetireAfter: time.Hour}}, fx.rotator.calls)
	rotate := fx.stepMessages(t, runID, "rotate")
	require.Contains(t, rotate[0], "the previous key retires at")
	require.Contains(t, rotate[1], "credentials v2")
	roll := fx.stepMessages(t, runID, "roll")
	require.Contains(t, roll[0], "waiting for web to restart")
	require.Contains(t, roll, "still rolling: web")
	require.Contains(t, roll, "every consumer runs with the new keypair")

	// A second run on the same environment is possible again: the slot is
	// free.
	again, err := fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "files", RetireAfter: time.Hour})
	require.NoError(t, err)
	require.NotEqual(t, runID, again)
}

func TestCreateRefusals(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()

	_, err := fx.control.Create(ctx, Input{EnvironmentID: uuid.Must(uuid.NewV7()), ServiceKey: "files"})
	require.ErrorIs(t, err, ErrEnvironmentNotFound)
	_, err = fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "uploads"})
	require.ErrorIs(t, err, ErrBucketNotFound)

	require.NoError(t, fx.db.FenceAllocation(ctx, fx.alloc.ID))
	_, err = fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "files"})
	require.ErrorIs(t, err, substrate.ErrBucketFenced)
	require.NoError(t, fx.db.UnfenceAllocation(ctx, fx.alloc.ID))

	owner := dbstore.ServiceOwner(fx.projectID, fx.envID, "demo", "production", "uploads")
	_, err = fx.db.EnsureBucketClaim(ctx, owner, dbstore.BucketSpec{
		Visibility: "private", StorageQuotaBytes: 1 << 30, Versioning: "disabled",
	})
	require.NoError(t, err)
	_, err = fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "uploads"})
	require.ErrorIs(t, err, substrate.ErrBucketNotProvisioned)

	// A run in flight (a deployment here) holds the slot; the refused
	// rotation leaves no run behind.
	other, err := fx.journal.CreateRun(ctx, journal.RunInput{Kind: "deployment", ProjectID: fx.projectID, EnvironmentID: fx.envID, Actor: "x"})
	require.NoError(t, err)
	require.NoError(t, fx.journal.StartRun(ctx, other.ID))
	_, err = fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "files"})
	require.ErrorIs(t, err, ErrRunInFlight)
	runs, err := fx.journal.ListRuns(ctx, fx.envID)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Empty(t, fx.rotator.calls, "nothing is committed before the run exists")
}

// An environment that is not active has no consumers to roll; the step is
// skipped and the run succeeds, since the rotation itself is done.
func TestRotationSkipsRollWhenNotActive(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()
	fx.status.old.State = deploy.EnvironmentStateDown
	fx.status.rolled = nil

	runID, err := fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "files", RetireAfter: time.Hour})
	require.NoError(t, err)
	require.NoError(t, fx.control.process(ctx, runID))
	require.Equal(t, "succeeded", fx.run(t, runID).Status)
	require.Equal(t, map[string]string{"rotate": "succeeded", "roll": "skipped"}, fx.steps(t, runID))
}

// Consumers that never roll fail the run after the roll timeout, with the
// retire instant in the reason: the rotation stands.
func TestRotationFailsWhenConsumersDoNotRoll(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()
	fx.status.rolled = nil

	runID, err := fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "files", RetireAfter: time.Hour})
	require.NoError(t, err)
	require.NoError(t, fx.control.process(ctx, runID))
	run := fx.run(t, runID)
	require.Equal(t, "failed", run.Status)
	require.NotNil(t, run.Failure)
	require.Contains(t, *run.Failure, "web did not roll in time")
	require.Contains(t, *run.Failure, "still retires at")
	require.Equal(t, map[string]string{"rotate": "succeeded", "roll": "failed"}, fx.steps(t, runID))
}

// A worker that never takes the commit fails the rotate step; the run
// explains that the substrate finishes on its own.
func TestRotationFailsWhenCommitIsNotTaken(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()
	fx.rotator.commit = false

	runID, err := fx.control.Create(ctx, Input{EnvironmentID: fx.envID, ServiceKey: "files", RetireAfter: time.Hour})
	require.NoError(t, err)
	require.NoError(t, fx.control.process(ctx, runID))
	run := fx.run(t, runID)
	require.Equal(t, "failed", run.Status)
	require.Contains(t, *run.Failure, "did not take the new keypair in time")
	require.Equal(t, map[string]string{"rotate": "failed"}, fx.steps(t, runID))
}

// A restart fails the rotation runs it orphaned and nothing else.
func TestRecoverOnBootFailsOrphanedRuns(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()

	orphan, err := fx.journal.CreateRun(ctx, journal.RunInput{Kind: Kind, ProjectID: fx.projectID, EnvironmentID: fx.envID, Actor: "x"})
	require.NoError(t, err)
	require.NoError(t, fx.journal.StartRun(ctx, orphan.ID))
	projects := project.New(fx.st)
	env2, err := projects.CreateEnvironment(ctx, fx.projectID, "staging", project.EnvironmentOptions{})
	require.NoError(t, err)
	deployment, err := fx.journal.CreateRun(ctx, journal.RunInput{Kind: "deployment", ProjectID: fx.projectID, EnvironmentID: env2.ID, Actor: "x"})
	require.NoError(t, err)
	require.NoError(t, fx.journal.StartRun(ctx, deployment.ID))

	require.NoError(t, fx.control.RecoverOnBoot(ctx))
	recovered := fx.run(t, orphan.ID)
	require.Equal(t, "failed", recovered.Status)
	require.Contains(t, *recovered.Failure, "the daemon restarted during the rotation")
	require.Equal(t, "running", fx.run(t, deployment.ID).Status)
}

func TestPendingConsumers(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	fresh := module.SourceStatus{State: module.SourceFresh}
	service := func(key string, health module.Health, pods ...reconcile.PodInfo) reconcile.ServiceStatus {
		return reconcile.ServiceStatus{Key: key, Type: "application", Health: health, Pods: pods}
	}
	newPod := reconcile.PodInfo{Name: "new", Started: since.Add(2 * time.Second)}
	sameSecond := reconcile.PodInfo{Name: "same", Started: since}
	oldPod := reconcile.PodInfo{Name: "old", Started: since.Add(-time.Minute)}

	cases := []struct {
		name     string
		status   *reconcile.Status
		expected []string
	}{
		{"stale observation", &reconcile.Status{Observation: module.SourceStatus{State: module.SourceStale},
			Services: []reconcile.ServiceStatus{service("web", module.HealthHealthy, newPod)}}, []string{"web"}},
		{"not projected", &reconcile.Status{Observation: fresh}, []string{"web"}},
		{"rolled", &reconcile.Status{Observation: fresh,
			Services: []reconcile.ServiceStatus{service("web", module.HealthHealthy, newPod, sameSecond)}}, nil},
		{"old pod draining", &reconcile.Status{Observation: fresh,
			Services: []reconcile.ServiceStatus{service("web", module.HealthHealthy, newPod, oldPod)}}, []string{"web"}},
		{"new pods, still progressing", &reconcile.Status{Observation: fresh,
			Services: []reconcile.ServiceStatus{service("web", module.HealthProgressing, newPod)}}, []string{"web"}},
		{"intercepted", &reconcile.Status{Observation: fresh,
			Services: []reconcile.ServiceStatus{{Key: "web", Intercepted: true, Health: module.HealthHealthy}}}, nil},
		{"scaled to zero", &reconcile.Status{Observation: fresh,
			Services: []reconcile.ServiceStatus{{Key: "web", Health: module.HealthUnknown,
				Diagnostics: []module.Diagnostic{{Code: "no-replicas"}}}}}, nil},
		{"no pods yet", &reconcile.Status{Observation: fresh,
			Services: []reconcile.ServiceStatus{{Key: "web", Health: module.HealthProgressing}}}, []string{"web"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, pendingConsumers(tc.status, []string{"web"}, since.Add(500*time.Millisecond)))
		})
	}
}
