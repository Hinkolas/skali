package journal

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/Hinkolas/skali/internal/project"
	"github.com/Hinkolas/skali/internal/redact"
	"github.com/Hinkolas/skali/internal/store"
	"github.com/Hinkolas/skali/internal/testdb"
)

type fixture struct {
	st            *store.Store
	svc           *Service
	projectID     uuid.UUID
	environmentID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	st := store.NewStore(pool)
	projects := project.New(st)
	proj, err := projects.Create(ctx, "demo", "", uuid.Nil)
	require.NoError(t, err)
	env, err := projects.CreateEnvironment(ctx, proj.ID, "production", project.EnvironmentOptions{})
	require.NoError(t, err)
	return &fixture{
		st:            st,
		svc:           NewService(st, "executor-1"),
		projectID:     proj.ID,
		environmentID: env.ID,
	}
}

func (f *fixture) startRun(t *testing.T) *store.Run {
	t.Helper()
	ctx := context.Background()
	run, err := f.svc.CreateRun(ctx, RunInput{
		Kind: "deployment", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, f.svc.StartRun(ctx, run.ID))
	return run
}

func TestRunLifecycleGuards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	run := f.startRun(t)

	// Invalid transitions are rejected at the store boundary.
	require.ErrorIs(t, f.svc.StartRun(ctx, run.ID), ErrInvalidTransition)
	require.ErrorIs(t, f.svc.FinishRun(ctx, run.ID, RunRunning), ErrInvalidTransition)

	// A second running run per environment is refused.
	second, err := f.svc.CreateRun(ctx, RunInput{
		Kind: "deployment", EnvironmentID: f.environmentID,
	})
	require.NoError(t, err)
	require.ErrorIs(t, f.svc.StartRun(ctx, second.ID), ErrRunConflict)

	require.NoError(t, f.svc.FinishRun(ctx, run.ID, RunSucceeded))
	require.NoError(t, f.svc.StartRun(ctx, second.ID))
	require.NoError(t, f.svc.FinishRun(ctx, second.ID, RunCancelled))

	// Terminal runs cannot be finished again.
	require.ErrorIs(t, f.svc.FinishRun(ctx, run.ID, RunFailed), ErrInvalidTransition)
	require.ErrorIs(t, f.svc.StartRun(ctx, uuid.New()), ErrNotFound)
}

// A run whose start lost the environment's running-run race is removed
// rather than left pending: nothing would ever finish it and the retention
// caps only reclaim terminal runs.
func TestDiscardRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	running := f.startRun(t)
	loser, err := f.svc.CreateRun(ctx, RunInput{
		Kind: "reconcile", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "system:reconcile",
	})
	require.NoError(t, err)
	require.ErrorIs(t, f.svc.StartRun(ctx, loser.ID), ErrRunConflict)

	require.NoError(t, f.svc.DiscardRun(ctx, loser.ID))
	_, err = f.st.GetRunByID(ctx, loser.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// A run that did start is never removed underneath its writer.
	require.NoError(t, f.svc.DiscardRun(ctx, running.ID))
	kept, err := f.st.GetRunByID(ctx, running.ID)
	require.NoError(t, err)
	require.Equal(t, string(RunRunning), kept.Status)

	require.ErrorIs(t, f.svc.DiscardRun(ctx, uuid.New()), ErrNotFound)
}

func TestStepAndAttemptGuards(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	run := f.startRun(t)
	step, err := f.svc.EnsureStep(ctx, run.ID, nil, "apply:web", "Apply web")
	require.NoError(t, err)
	require.Equal(t, "pending", step.Status)

	// EnsureStep is idempotent on (run, key).
	again, err := f.svc.EnsureStep(ctx, run.ID, nil, "apply:web", "Apply web")
	require.NoError(t, err)
	require.Equal(t, step.ID, again.ID)

	require.NoError(t, f.svc.SetStepStatus(ctx, step.ID, StepRunning))
	require.ErrorIs(t, f.svc.SetStepStatus(ctx, step.ID, StepPending), ErrInvalidTransition)

	attempt, err := f.svc.StartAttempt(ctx, step.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), attempt.Number)
	require.Equal(t, "executor-1", attempt.ExecutorID)

	// One running attempt per step; a new attempt starts only after the
	// previous one is terminal.
	_, err = f.svc.StartAttempt(ctx, step.ID)
	require.ErrorIs(t, err, ErrAttemptConflict)
	require.NoError(t, f.svc.FinishAttempt(ctx, attempt.ID, AttemptFailed))
	require.ErrorIs(t, f.svc.FinishAttempt(ctx, attempt.ID, AttemptSucceeded), ErrInvalidTransition)

	retry, err := f.svc.StartAttempt(ctx, step.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), retry.Number)
	require.NoError(t, f.svc.FinishAttempt(ctx, retry.ID, AttemptSucceeded))
	require.NoError(t, f.svc.SetStepStatus(ctx, step.ID, StepSucceeded))
	require.NoError(t, f.svc.FinishRun(ctx, run.ID, RunSucceeded))
}

func TestFinishRunForcesTerminality(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	run := f.startRun(t)
	running, err := f.svc.EnsureStep(ctx, run.ID, nil, "running-step", "Running")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, running.ID, StepRunning))
	attempt, err := f.svc.StartAttempt(ctx, running.ID)
	require.NoError(t, err)
	_, err = f.svc.EnsureStep(ctx, run.ID, nil, "pending-step", "Pending")
	require.NoError(t, err)
	waiting, err := f.svc.EnsureStep(ctx, run.ID, nil, "waiting-step", "Waiting")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, waiting.ID, StepWaiting))

	require.NoError(t, f.svc.FinishRun(ctx, run.ID, RunFailed))

	tree, err := f.svc.RunTree(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", tree.Run.Status)
	statuses := map[string]string{}
	for _, node := range tree.Steps {
		statuses[node.Step.Key] = node.Step.Status
	}
	require.Equal(t, map[string]string{
		"running-step": "failed",
		"pending-step": "skipped",
		"waiting-step": "skipped",
	}, statuses)

	closed, err := f.st.GetAttemptByID(ctx, attempt.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", closed.Status)
	require.NotNil(t, closed.FinishedAt)
}

// Exit criterion: secrets cannot appear in run logs. The writer redacts
// messages and string fields before anything reaches the table.
func TestAppendRedactsSecrets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	run := f.startRun(t)
	step, err := f.svc.EnsureStep(ctx, run.ID, nil, "apply", "Apply")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, step.ID, StepRunning))
	attempt, err := f.svc.StartAttempt(ctx, step.ID)
	require.NoError(t, err)

	writer := f.svc.Writer(attempt.ID, redact.New(map[string]string{
		"log-plant-secret-value": "SESSION_SECRET",
	}))
	require.NoError(t, writer.Info(ctx, "connecting with log-plant-secret-value"))
	require.NoError(t, writer.Log(ctx, "error", "failed", map[string]any{
		"detail": "token log-plant-secret-value rejected",
		"count":  3,
	}))

	var combined string
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT string_agg(message, ' ') || ' ' || string_agg(fields::text, ' ') FROM run_logs").Scan(&combined))
	require.NotContains(t, combined, "log-plant-secret-value")
	require.Contains(t, combined, "[redacted:SESSION_SECRET]")
}

func TestLogCaps(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	run := f.startRun(t)
	step, err := f.svc.EnsureStep(ctx, run.ID, nil, "apply", "Apply")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, step.ID, StepRunning))
	attempt, err := f.svc.StartAttempt(ctx, step.ID)
	require.NoError(t, err)

	// Oversized messages are truncated to the byte cap.
	require.NoError(t, f.svc.Append(ctx, attempt.ID, nil, "info", strings.Repeat("x", MaxEntryBytes+100), nil))
	var message string
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT message FROM run_logs WHERE attempt_id = $1 AND seq = 1", attempt.ID).Scan(&message))
	require.Len(t, message, MaxEntryBytes)
	require.True(t, strings.HasSuffix(message, "... [truncated]"))

	// Fill up to the cap: the final slot becomes the truncation marker and
	// later appends are dropped.
	_, err = f.st.Pool.Exec(ctx, `INSERT INTO run_logs (id, attempt_id, seq, level, message)
		SELECT gen_random_uuid(), $1, seq, 'info', 'filler'
		FROM generate_series(2, $2) AS seq`, attempt.ID, MaxEntriesPerAttempt-2)
	require.NoError(t, err)
	require.NoError(t, f.svc.Append(ctx, attempt.ID, nil, "info", "last regular entry", nil))
	require.NoError(t, f.svc.Append(ctx, attempt.ID, nil, "info", "this becomes the marker", nil))
	require.NoError(t, f.svc.Append(ctx, attempt.ID, nil, "info", "this is dropped", nil))

	var count, maxSeq int
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT count(*), max(seq) FROM run_logs WHERE attempt_id = $1", attempt.ID).Scan(&count, &maxSeq))
	require.Equal(t, MaxEntriesPerAttempt, count)
	require.Equal(t, MaxEntriesPerAttempt, maxSeq)
	var marker string
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT message FROM run_logs WHERE attempt_id = $1 AND seq = $2",
		attempt.ID, MaxEntriesPerAttempt).Scan(&marker))
	require.Equal(t, "log truncated: entry cap reached", marker)

	// Appends to terminal attempts are rejected.
	require.NoError(t, f.svc.FinishAttempt(ctx, attempt.ID, AttemptSucceeded))
	require.ErrorIs(t, f.svc.Append(ctx, attempt.ID, nil, "info", "late", nil), ErrAttemptTerminal)
}

func TestPruneKeepsNewestRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	for range MaxRunsPerEnvironment + 5 {
		run := f.startRun(t)
		require.NoError(t, f.svc.FinishRun(ctx, run.ID, RunSucceeded))
	}
	runs, err := f.svc.ListRuns(ctx, f.environmentID)
	require.NoError(t, err)
	require.Len(t, runs, MaxRunsPerEnvironment)
}

func TestRecoverOnBoot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	run := f.startRun(t)
	step, err := f.svc.EnsureStep(ctx, run.ID, nil, "apply:web", "Apply web")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, step.ID, StepRunning))
	attempt, err := f.svc.StartAttempt(ctx, step.ID)
	require.NoError(t, err)

	// A new daemon boot with a fresh executor identity over the same pool.
	restarted := NewService(f.st, "executor-2")
	failed, err := restarted.RecoverOnBoot(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), failed)

	// The orphaned attempt is failed with a restart diagnostic as its
	// final entry; run and step keep their statuses.
	closed, err := f.st.GetAttemptByID(ctx, attempt.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", closed.Status)
	events, err := restarted.StepLogs(ctx, step.ID, Cursor{}, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Contains(t, events[0].Message, "daemon restarted")
	tree, err := restarted.RunTree(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "running", tree.Run.Status)
	require.Equal(t, "running", tree.Steps[0].Step.Status)

	// Reattach by deterministic key and continue: same step, next attempt.
	same, err := restarted.EnsureStep(ctx, run.ID, nil, "apply:web", "Apply web")
	require.NoError(t, err)
	require.Equal(t, step.ID, same.ID)
	next, err := restarted.StartAttempt(ctx, step.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), next.Number)

	// Idempotent: a second recovery finds nothing.
	failed, err = restarted.RecoverOnBoot(ctx)
	require.NoError(t, err)
	require.Zero(t, failed)
}

func TestSubscribeDeliversBacklogAndLive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	run := f.startRun(t)
	step, err := f.svc.EnsureStep(ctx, run.ID, nil, "apply", "Apply")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, step.ID, StepRunning))
	attempt, err := f.svc.StartAttempt(ctx, step.ID)
	require.NoError(t, err)

	require.NoError(t, f.svc.Append(ctx, attempt.ID, nil, "info", "before subscribe", nil))
	subscription, err := f.svc.Subscribe(ctx, step.ID, Cursor{})
	require.NoError(t, err)
	defer subscription.Cancel()
	require.Len(t, subscription.Backlog, 1)
	require.Equal(t, "before subscribe", subscription.Backlog[0].Message)

	require.NoError(t, f.svc.Append(ctx, attempt.ID, nil, "info", "after subscribe", nil))
	event := <-subscription.Events
	require.Equal(t, "after subscribe", event.Message)
	require.Equal(t, int64(2), event.Seq)

	_, err = f.svc.Subscribe(ctx, uuid.New(), Cursor{})
	require.ErrorIs(t, err, ErrNotFound)
}
