package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/journal"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
	"github.com/Hinkolas/skali/internal/valuestore"
)

type serviceFixture struct {
	st            *store.Store
	journal       *journal.Service
	controller    *Controller
	projectID     uuid.UUID
	environmentID uuid.UUID
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	ctx := context.Background()
	st := store.NewStore(testdb.New(t))
	journalSvc := journal.NewService(st, uuid.NewString())
	values, err := valuestore.New(st, strings.Repeat("s", 32))
	require.NoError(t, err)
	targets, err := NewTargetStore(st, strings.Repeat("s", 32))
	require.NoError(t, err)

	project, err := st.CreateProject(ctx, store.CreateProjectParams{
		ID: uuid.New(), Name: "demo", DisplayName: "Demo", SourceMode: "file",
	})
	require.NoError(t, err)
	environment, err := st.CreateEnvironment(ctx, store.CreateEnvironmentParams{
		ID: uuid.New(), ProjectID: project.ID, Name: "production", MaxRole: "admin", Priority: "normal",
	})
	require.NoError(t, err)

	return &serviceFixture{
		st:      st,
		journal: journalSvc,
		controller: New(Deps{
			Store: st, Journal: journalSvc, Values: values, Targets: targets,
		}, Config{}),
		projectID:     project.ID,
		environmentID: environment.ID,
	}
}

// RecoverOnBoot closes what a dead daemon left behind: the row fails with a
// restart diagnostic and the run is driven terminal. Backups are not
// resumable by design.
func TestRecoverOnBootFailsUnfinishedRows(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()

	run, err := f.journal.CreateRun(ctx, journal.RunInput{
		Kind: KindBackup, ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.journal.StartRun(ctx, run.ID))
	runID := run.ID
	row, err := f.st.CreateBackup(ctx, store.CreateBackupParams{
		ID: uuid.New(), Kind: KindBackup, EnvironmentID: f.environmentID,
		ProjectName: "demo", EnvironmentName: "production", RunID: &runID,
	})
	require.NoError(t, err)
	claimed, err := f.st.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, ToStatus: StatusRunning, FromStatus: StatusPending,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, claimed)

	require.NoError(t, f.controller.RecoverOnBoot(ctx))

	recovered, err := f.st.GetBackup(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, recovered.Status)
	require.NotNil(t, recovered.Error)
	require.Contains(t, *recovered.Error, "daemon restarted")
	require.NotNil(t, recovered.FinishedAt)

	finished, err := f.journal.Run(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, string(journal.RunFailed), finished.Status)

	// A second boot is a no-op: nothing unfinished remains.
	require.NoError(t, f.controller.RecoverOnBoot(ctx))
}

// The from-status guard makes the running claim exclusive: a lost race is
// zero rows, never a silent double execution.
func TestBackupStatusGuard(t *testing.T) {
	f := newServiceFixture(t)
	ctx := context.Background()

	row, err := f.st.CreateBackup(ctx, store.CreateBackupParams{
		ID: uuid.New(), Kind: KindBackup, EnvironmentID: f.environmentID,
		ProjectName: "demo", EnvironmentName: "production",
	})
	require.NoError(t, err)

	first, err := f.st.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, ToStatus: StatusRunning, FromStatus: StatusPending,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, first)

	second, err := f.st.SetBackupStatus(ctx, store.SetBackupStatusParams{
		ID: row.ID, ToStatus: StatusRunning, FromStatus: StatusPending,
	})
	require.NoError(t, err)
	require.Zero(t, second, "a second claim must lose")
}
