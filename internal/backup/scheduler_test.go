package backup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/cron"
	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/store"
)

// fakeRevisions serves revision documents from memory.
type fakeRevisions map[uuid.UUID]*revision.Revision

func (f fakeRevisions) GetRevision(_ context.Context, id uuid.UUID) (*revision.Revision, error) {
	rev, ok := f[id]
	if !ok {
		return nil, ErrEnvironmentNotFound
	}
	return rev, nil
}

// activate gives the fixture environment an active revision carrying the
// definition, the way a converged deployment would, and registers the
// document with the controller's revision loader.
func (f *serviceFixture) activate(t *testing.T, definition compiler.ProjectDefinition) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	versionID, revisionID := uuid.New(), uuid.New()
	encoded, err := json.Marshal(definition)
	require.NoError(t, err)
	_, err = f.st.InsertDefinitionVersion(ctx, store.InsertDefinitionVersionParams{
		ID: versionID, ProjectID: f.projectID, SchemaVersion: "1", DefinitionHash: revisionID.String(),
		Definition: encoded, Source: []byte("{}"), Format: "json", CompilerVersion: "test",
	})
	require.NoError(t, err)
	_, err = f.st.InsertRevision(ctx, store.InsertRevisionParams{
		ID: revisionID, ProjectID: f.projectID, EnvironmentID: f.environmentID, DefinitionVersionID: versionID,
		SchemaVersion: "1", Checksum: revisionID.String(), DefinitionHash: revisionID.String(),
		ValuesHash: "values", CompilerVersion: "test", Document: []byte("{}"),
	})
	require.NoError(t, err)
	_, _ = f.st.Pool.Exec(ctx, "INSERT INTO environment_targets (environment_id) VALUES ($1) ON CONFLICT DO NOTHING", f.environmentID)
	_, err = f.st.SetEnvironmentTarget(ctx, store.SetEnvironmentTargetParams{EnvironmentID: f.environmentID, TargetRevisionID: &revisionID})
	require.NoError(t, err)
	_, err = f.st.SetEnvironmentActiveRevision(ctx, store.SetEnvironmentActiveRevisionParams{EnvironmentID: f.environmentID, ActiveRevisionID: &revisionID})
	require.NoError(t, err)

	revisions, ok := f.controller.revisions.(fakeRevisions)
	if !ok {
		revisions = fakeRevisions{}
		f.controller.revisions = revisions
	}
	revisions[revisionID] = &revision.Revision{
		Project: "demo", Environment: "production", Definition: definition, Checksum: revisionID.String(),
	}
	return revisionID
}

func (f *serviceFixture) configureTarget(t *testing.T) {
	t.Helper()
	_, err := f.controller.deps.Targets.Upsert(context.Background(), TargetInput{
		Name: DefaultTargetName, Endpoint: "http://s3.test", Bucket: "backups",
		AccessKeyID: "key", SecretAccessKey: "secret",
	})
	require.NoError(t, err)
}

func databaseDefinition() compiler.ProjectDefinition {
	return compiler.ProjectDefinition{
		Schema: compiler.DefinitionSchema, Name: "demo",
		Databases: map[string]compiler.DatabaseClaim{"data": {Engine: "postgres"}},
	}
}

// setSchedule stores the environment's automatic backup setting the way
// the project service does; an empty expression turns it off.
func (f *serviceFixture) setSchedule(t *testing.T, expr string, retentionSeconds int64) {
	t.Helper()
	_, err := f.st.Pool.Exec(context.Background(),
		"UPDATE environments SET backup_schedule = $1, backup_retention_seconds = $2 WHERE id = $3",
		expr, retentionSeconds, f.environmentID)
	require.NoError(t, err)
}

const week = 7 * 86400

func newTestScheduler(f *serviceFixture, now *time.Time) *Scheduler {
	s := NewScheduler(f.controller)
	s.now = func() time.Time { return *now }
	return s
}

func TestNextDue(t *testing.T) {
	daily, err := cron.Parse("0 3 * * *")
	require.NoError(t, err)
	base := time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC)

	_, due := nextDue(daily, base, base.Add(20*time.Hour))
	require.False(t, due, "the next fire is still ahead")

	fire, due := nextDue(daily, base, base.Add(24*time.Hour+time.Second))
	require.True(t, due)
	require.True(t, base.Add(24*time.Hour).Equal(fire))

	// Three missed days collapse into the most recent fire: one catch-up.
	fire, due = nextDue(daily, base, base.Add(72*time.Hour+30*time.Minute))
	require.True(t, due)
	require.True(t, base.Add(72*time.Hour).Equal(fire))
}

func TestSchedulerIdlesWithoutTarget(t *testing.T) {
	f := newServiceFixture(t)
	f.activate(t, databaseDefinition())
	f.setSchedule(t, "0 3 * * *", week)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s := newTestScheduler(f, &now)

	require.NoError(t, s.Tick(context.Background()))
	rows, err := f.st.ListBackupSchedules(context.Background())
	require.NoError(t, err)
	require.Empty(t, rows, "no target means nothing to schedule against")
}

func TestSchedulerSeedsThenFiresOnce(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	f.activate(t, databaseDefinition())
	f.setSchedule(t, "0 3 * * *", week)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s := newTestScheduler(f, &now)

	// First sight seeds the schedule at its next fire; nothing runs yet.
	require.NoError(t, s.Tick(ctx))
	rows, err := f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "0 3 * * *", rows[0].Schedule)
	require.True(t, now.Equal(rows[0].LastFireAt))
	require.Nil(t, rows[0].LastBackupID)
	unfinished, err := f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Empty(t, unfinished)

	// Still before 03:00 the next day: quiet.
	now = now.Add(14 * time.Hour)
	require.NoError(t, s.Tick(ctx))
	unfinished, err = f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Empty(t, unfinished)

	// Past the fire: one scheduled backup, marked by trigger and actor and
	// carrying the retention in force.
	now = now.Add(2 * time.Hour)
	require.NoError(t, s.Tick(ctx))
	unfinished, err = f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
	row := unfinished[0]
	require.Equal(t, TriggerScheduled, row.Trigger)
	require.Equal(t, StrategyComplete, row.Strategy)
	require.EqualValues(t, week, row.RetentionSeconds)
	require.NotNil(t, row.RunID)
	run, err := f.journal.Run(ctx, *row.RunID)
	require.NoError(t, err)
	require.Equal(t, KindBackup, run.Kind)
	require.Equal(t, ScheduleActor, run.Actor)

	rows, err = f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, time.Date(2026, 9, 17, 3, 0, 0, 0, time.UTC).Equal(rows[0].LastFireAt))
	require.NotNil(t, rows[0].LastBackupID)
	require.Equal(t, row.ID, *rows[0].LastBackupID)

	// The same minute again creates nothing (the run is also still in
	// flight, but due-ness alone already says no).
	require.NoError(t, s.Tick(ctx))
	unfinished, err = f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
}

func TestSchedulerHoldsFireWhileEnvironmentBusy(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	f.activate(t, databaseDefinition())
	f.setSchedule(t, "0 3 * * *", week)
	now := time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)
	s := newTestScheduler(f, &now)
	require.NoError(t, s.Tick(ctx))
	seeded := now

	// A deployment owns the environment's running-run slot.
	deployment, err := f.journal.CreateRun(ctx, journal.RunInput{
		Kind: "deployment", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.journal.StartRun(ctx, deployment.ID))

	now = now.Add(2 * time.Hour)
	require.NoError(t, s.Tick(ctx))
	unfinished, err := f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Empty(t, unfinished, "the fire waits while another run holds the environment")
	rows, err := f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.True(t, seeded.Equal(rows[0].LastFireAt), "a held fire does not advance the schedule")

	// The deployment finishes; the next tick catches up.
	require.NoError(t, f.journal.FinishRun(ctx, deployment.ID, journal.RunSucceeded))
	now = now.Add(time.Minute)
	require.NoError(t, s.Tick(ctx))
	unfinished, err = f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
	require.Equal(t, TriggerScheduled, unfinished[0].Trigger)
}

// A changed expression reseeds at the new schedule's next fire instead of
// catching up on the old one.
func TestSchedulerReseedsOnExpressionChange(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	f.activate(t, databaseDefinition())
	f.setSchedule(t, "0 3 * * *", week)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s := newTestScheduler(f, &now)
	require.NoError(t, s.Tick(ctx))

	// Well past 03:00 the next day the setting moves to 04:00: the row is
	// reseeded at now and nothing fires for the missed 03:00.
	now = now.Add(20 * time.Hour)
	f.setSchedule(t, "0 4 * * *", week)
	require.NoError(t, s.Tick(ctx))
	rows, err := f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "0 4 * * *", rows[0].Schedule)
	require.True(t, now.Equal(rows[0].LastFireAt))
	unfinished, err := f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Empty(t, unfinished)

	// Past the new fire: exactly one backup.
	now = now.Add(24 * time.Hour)
	require.NoError(t, s.Tick(ctx))
	unfinished, err = f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
}

func TestSchedulerDropsDisabledSchedule(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	f.activate(t, databaseDefinition())
	f.setSchedule(t, "0 3 * * *", week)
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	s := newTestScheduler(f, &now)
	require.NoError(t, s.Tick(ctx))
	rows, err := f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// Automatic backups turned off: the state row goes with it.
	f.setSchedule(t, "", 0)
	require.NoError(t, s.Tick(ctx))
	rows, err = f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)

	// Turned on again and then the environment leaves the active state:
	// the row goes as well.
	f.setSchedule(t, "0 3 * * *", week)
	require.NoError(t, s.Tick(ctx))
	rows, err = f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, err = f.st.Pool.Exec(ctx, "UPDATE environment_targets SET state = 'down' WHERE environment_id = $1", f.environmentID)
	require.NoError(t, err)
	require.NoError(t, s.Tick(ctx))
	rows, err = f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestPlanComponentsOrders(t *testing.T) {
	definition := compiler.ProjectDefinition{
		Databases: map[string]compiler.DatabaseClaim{"main": {}, "analytics": {}},
		Buckets:   map[string]compiler.BucketClaim{"files": {}},
		Applications: map[string]compiler.Application{
			"web": {Volumes: map[string]compiler.Volume{"cache": {}, "uploads": {}}},
		},
	}
	labels := func(components []Component) []string {
		out := make([]string, 0, len(components))
		for _, c := range components {
			out = append(out, componentLabel(c))
		}
		return out
	}
	require.Equal(t, []string{"db:analytics", "db:main", "bucket:files", "volume:web.cache", "volume:web.uploads"},
		labels(planComponents(&definition)), "every snapshot takes everything, in a fixed order")
}

// An environment whose revision declares nothing stateful is skipped at
// its fire instead of producing a failed run every time it is due.
func TestSchedulerSkipsEnvironmentWithNothingToBackUp(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()
	f.configureTarget(t)
	f.activate(t, compiler.ProjectDefinition{
		Schema: compiler.DefinitionSchema, Name: "demo",
		Applications: map[string]compiler.Application{"web": {}},
	})
	f.setSchedule(t, "0 3 * * *", week)
	now := time.Date(2026, 9, 16, 2, 0, 0, 0, time.UTC)
	s := newTestScheduler(f, &now)
	require.NoError(t, s.Tick(ctx))

	now = now.Add(2 * time.Hour)
	require.NoError(t, s.Tick(ctx))
	unfinished, err := f.st.ListUnfinishedBackups(ctx)
	require.NoError(t, err)
	require.Empty(t, unfinished)
	rows, err := f.st.ListBackupSchedules(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].LastBackupID)
}
