package journal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

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
	require.ErrorIs(t, f.svc.FinishRun(ctx, uuid.New(), RunSucceeded), ErrNotFound)

	// A queued run can only be cancelled.
	queued, err := f.svc.CreateRun(ctx, RunInput{Kind: "deployment", EnvironmentID: f.environmentID})
	require.NoError(t, err)
	require.ErrorIs(t, f.svc.FinishRun(ctx, queued.ID, RunSucceeded), ErrInvalidTransition)
	require.NoError(t, f.svc.FinishRun(ctx, queued.ID, RunCancelled))
	row, err := f.svc.Run(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", row.Status)
	require.NotNil(t, row.FinishedAt)
}

// A run begins running in one statement, and one that loses the
// environment's running-run race leaves no row behind: nothing would ever
// finish it and the retention caps only reclaim terminal runs.
func TestBeginRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	input := RunInput{Kind: "reconcile", ProjectID: f.projectID, EnvironmentID: f.environmentID, Actor: "system:reconcile"}

	running, err := f.svc.BeginRun(ctx, input)
	require.NoError(t, err)
	require.Equal(t, string(RunRunning), running.Status)
	require.NotNil(t, running.StartedAt)

	_, err = f.svc.BeginRun(ctx, input)
	require.ErrorIs(t, err, ErrRunConflict)
	runs, err := f.st.ListRunsByEnvironment(ctx, &f.environmentID)
	require.NoError(t, err)
	require.Len(t, runs, 1)

	require.NoError(t, f.svc.FinishRun(ctx, running.ID, RunSucceeded))
	next, err := f.svc.BeginRun(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, running.ID, next.ID)
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

	require.ErrorIs(t, f.svc.SetStepStatus(ctx, uuid.New(), StepRunning), ErrNotFound)
	require.ErrorIs(t, f.svc.FinishAttempt(ctx, uuid.New(), AttemptSucceeded), ErrNotFound)
}

// Concurrent writers ensuring one step all get the same row, including
// those whose statement snapshot predates the winner's insert.
func TestEnsureStepConcurrently(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	run := f.startRun(t)

	ids := make([]uuid.UUID, 8)
	var group errgroup.Group
	for i := range ids {
		group.Go(func() error {
			step, err := f.svc.EnsureStep(ctx, run.ID, nil, "apply:web", "Apply web")
			if err == nil {
				ids[i] = step.ID
			}
			return err
		})
	}
	require.NoError(t, group.Wait())
	for _, id := range ids {
		require.Equal(t, ids[0], id)
	}
}

// RecordStep creates a step by key or moves the existing one as the step
// machine allows, writing one finished attempt per record in the same
// statement; a deduplicated record whose line is unchanged adds nothing.
func TestRecordStep(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	run := f.startRun(t)
	redactor := redact.New(map[string]string{"s3cr3t": "DB_PASSWORD"})
	record := func(key string, to StepStatus, line string, dedupe bool) (StepState, error) {
		return f.svc.RecordStep(ctx, StepRecord{RunID: run.ID, Key: key, Title: key, To: to,
			Entries: []LogEntry{{Level: "info", Message: line}}, Dedupe: dedupe}, redactor)
	}
	lines := func(stepID uuid.UUID) []string {
		events, err := f.svc.StepLogs(ctx, stepID, Cursor{}, 10)
		require.NoError(t, err)
		messages := make([]string, 0, len(events))
		for _, event := range events {
			messages = append(messages, event.Message)
		}
		return messages
	}

	// A new step waits with its redacted line; the same line again writes
	// nothing, and a new one appends.
	waiting, err := record("verify", StepWaiting, "pool starting with s3cr3t", true)
	require.NoError(t, err)
	require.Equal(t, StepState{ID: waiting.ID, Status: StepWaiting,
		Latest: "pool starting with [redacted:DB_PASSWORD]"}, waiting)
	again, err := record("verify", StepWaiting, "pool starting with s3cr3t", true)
	require.NoError(t, err)
	require.Equal(t, waiting, again)
	_, err = record("verify", StepWaiting, "database applying", true)
	require.NoError(t, err)
	require.Equal(t, []string{"pool starting with [redacted:DB_PASSWORD]", "database applying"}, lines(waiting.ID))
	states, err := f.svc.StepStates(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, map[string]StepState{
		"verify": {ID: waiting.ID, Status: StepWaiting, Latest: "database applying"},
	}, states)

	// Succeeding with the unchanged line moves the step as if through
	// running and adds no line; a terminal step is refused.
	done, err := record("verify", StepSucceeded, "database applying", true)
	require.NoError(t, err)
	require.Equal(t, StepSucceeded, done.Status)
	require.Len(t, lines(waiting.ID), 2)
	tree, err := f.svc.RunTree(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", tree.Steps[0].Step.Status)
	require.NotNil(t, tree.Steps[0].Step.StartedAt)
	require.NotNil(t, tree.Steps[0].Step.FinishedAt)
	refused, err := record("verify", StepWaiting, "late", true)
	require.ErrorIs(t, err, ErrInvalidTransition)
	require.Equal(t, StepState{ID: waiting.ID, Status: StepSucceeded}, refused)
	require.Len(t, lines(waiting.ID), 2)

	// A step with a running attempt is refused.
	busy, err := f.svc.EnsureStep(ctx, run.ID, nil, "apply:worker", "Apply worker")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, busy.ID, StepRunning))
	_, err = f.svc.StartAttempt(ctx, busy.ID)
	require.NoError(t, err)
	_, err = record("apply:worker", StepSucceeded, "applied", false)
	require.ErrorIs(t, err, ErrAttemptConflict)

	// A completed step is created under its parent with a failed attempt.
	failed, err := f.svc.RecordStep(ctx, StepRecord{RunID: run.ID, ParentID: &busy.ID, Key: "apply:web",
		Title: "Apply web", To: StepFailed, Entries: []LogEntry{{Level: "error", Message: "boom"}}}, nil)
	require.NoError(t, err)
	step, err := f.st.GetStepByID(ctx, failed.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", step.Status)
	require.Equal(t, &busy.ID, step.ParentID)
	var status string
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT status FROM attempts WHERE step_id = $1", failed.ID).Scan(&status))
	require.Equal(t, "failed", status)
	require.Equal(t, []string{"boom"}, lines(failed.ID))

	// A step that starts is created running without an attempt, and its
	// outcome lands as the one finished attempt, published live with
	// redacted fields.
	started, err := f.svc.RecordStep(ctx, StepRecord{RunID: run.ID, Key: "revision", Title: "Create revision",
		To: StepRunning}, nil)
	require.NoError(t, err)
	require.Equal(t, StepRunning, started.Status)
	var attempts int
	require.NoError(t, f.st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM attempts WHERE step_id = $1", started.ID).Scan(&attempts))
	require.Zero(t, attempts)
	_, err = f.svc.RecordStep(ctx, StepRecord{RunID: run.ID, Key: "revision", To: StepRunning,
		Entries: []LogEntry{{Level: "info", Message: "early"}}}, nil)
	require.Error(t, err, "a step that starts writes no lines")
	subscription, err := f.svc.Subscribe(ctx, started.ID, Cursor{})
	require.NoError(t, err)
	defer subscription.Cancel()
	_, err = f.svc.RecordStep(ctx, StepRecord{RunID: run.ID, Key: "revision", To: StepSucceeded, Entries: []LogEntry{
		{Level: "info", Message: "stored with s3cr3t"},
		{Level: "warn", Message: "slow", Fields: map[string]any{"detail": "s3cr3t", "count": 2}},
	}}, redactor)
	require.NoError(t, err)
	events, err := f.svc.StepLogs(ctx, started.ID, Cursor{}, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, int64(1), events[0].AttemptNumber)
	require.Equal(t, []int64{1, 2}, []int64{events[0].Seq, events[1].Seq})
	require.Equal(t, "stored with [redacted:DB_PASSWORD]", events[0].Message)
	require.Equal(t, "warn", events[1].Level)
	require.JSONEq(t, `{"count": 2, "detail": "[redacted:DB_PASSWORD]"}`, string(events[1].Fields))
	live := <-subscription.Events
	require.Equal(t, events[0].Message, live.Message)
	require.Equal(t, int64(1), live.Seq)
}

// A record whose statement snapshot predates another writer's insert of
// the step neither finds nor creates it; it records again and lands on the
// step that writer created.
func TestRecordStepAfterConcurrentInsert(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	run := f.startRun(t)

	tx, err := f.st.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	inserted := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO steps (id, run_id, key, title, status)
		VALUES ($1, $2, 'verify', 'Verify health', 'waiting')`, inserted, run.ID)
	require.NoError(t, err)

	type result struct {
		state StepState
		err   error
	}
	recorded := make(chan result, 1)
	go func() {
		state, err := f.svc.RecordStep(ctx, StepRecord{RunID: run.ID, Key: "verify", Title: "Verify health",
			To: StepWaiting, Entries: []LogEntry{{Level: "info", Message: "waiting"}}, Dedupe: true}, nil)
		recorded <- result{state, err}
	}()
	// The record's insert waits on the uncommitted one.
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting))
		return waiting > 0
	}, 10*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit(ctx))

	got := <-recorded
	require.NoError(t, got.err)
	require.Equal(t, inserted, got.state.ID)
	events, err := f.svc.StepLogs(ctx, inserted, Cursor{}, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
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

// waitUntilBlocked waits until a statement whose text holds marker waits
// on a lock, or until done delivered: a statement that should have waited
// and did not then fails on what it wrote, not on the wait.
func waitUntilBlocked[T any](t *testing.T, st *store.Store, marker string, done chan T) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%'`,
			marker).Scan(&waiting))
		return waiting > 0 || len(done) > 0
	}, 10*time.Second, 10*time.Millisecond)
}

// A finish waits for a step and an attempt whose inserts have not
// committed yet, and then closes them with the rest of the run: its
// snapshot never misses work written under the run before it.
func TestFinishRunWaitsForUncommittedSteps(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	run := f.startRun(t)

	tx, err := f.st.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	pending, running, attempt := uuid.New(), uuid.New(), uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO steps (id, run_id, key, title) VALUES ($1, $2, 'pending', 'Pending')`,
		pending, run.ID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO steps (id, run_id, key, title, status, started_at)
		VALUES ($1, $2, 'running', 'Running', 'running', now())`, running, run.ID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO attempts (id, step_id, number, executor_id) VALUES ($1, $2, 1, 'other')`,
		attempt, running)
	require.NoError(t, err)

	finished := make(chan error, 1)
	go func() { finished <- f.svc.FinishRun(ctx, run.ID, RunCancelled) }()
	waitUntilBlocked(t, f.st, "LockRunForFinish", finished)
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, <-finished)

	for id, want := range map[uuid.UUID]string{pending: "skipped", running: "cancelled"} {
		step, err := f.st.GetStepByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, step.Status)
	}
	closed, err := f.st.GetAttemptByID(ctx, attempt)
	require.NoError(t, err)
	require.Equal(t, "cancelled", closed.Status)
}

// Nothing is journaled under a finished run: a step write that arrives
// while the finish is committing waits for it and then writes nothing, and
// so does every later one. The refusal is an invalid transition, so
// callers that tolerate a refused step tolerate it too.
func TestNothingIsJournaledUnderAFinishedRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	run := f.startRun(t)
	existing, err := f.svc.EnsureStep(ctx, run.ID, nil, "existing", "Existing")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, existing.ID, StepRunning))

	// A finish in flight holds the run row until it commits.
	tx, err := f.st.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, run.ID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE runs SET status = 'cancelled', finished_at = now() WHERE id = $1`, run.ID)
	require.NoError(t, err)

	ensured := make(chan error, 1)
	go func() {
		_, err := f.svc.EnsureStep(ctx, run.ID, nil, "late", "Late")
		ensured <- err
	}()
	waitUntilBlocked(t, f.st, "EnsureStep", ensured)
	require.NoError(t, tx.Commit(ctx))
	require.ErrorIs(t, <-ensured, ErrRunFinished)

	_, err = f.svc.RecordStep(ctx, StepRecord{RunID: run.ID, Key: "later", Title: "Later", To: StepSucceeded,
		Entries: []LogEntry{{Level: "info", Message: "done"}}}, nil)
	require.ErrorIs(t, err, ErrRunFinished)
	require.ErrorIs(t, err, ErrInvalidTransition)
	_, err = f.svc.RecordStep(ctx, StepRecord{RunID: run.ID, Key: "existing", Title: "Existing", To: StepSucceeded}, nil)
	require.ErrorIs(t, err, ErrRunFinished)
	_, err = f.svc.StartAttempt(ctx, existing.ID)
	require.ErrorIs(t, err, ErrRunFinished)

	tree, err := f.svc.RunTree(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, tree.Steps, 1)
	require.Equal(t, "running", tree.Steps[0].Step.Status, "the raw finish above closed nothing")
	require.Empty(t, tree.Steps[0].Attempts)
}

// FailRun records the one-line reason lists and closing lines show; other
// terminal statuses never carry one, and the reason is redacted and bounded
// like a log line.
func TestFailRunRecordsReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	run := f.startRun(t)
	redactor := redact.New(map[string]string{"s3cr3t": "DB_PASSWORD"})
	require.NoError(t, f.svc.FailRun(ctx, run.ID, redactor, "  pg_dump: password s3cr3t rejected\n"))
	tree, err := f.svc.RunTree(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", tree.Run.Status)
	require.NotNil(t, tree.Run.Failure)
	require.Equal(t, "pg_dump: password [redacted:DB_PASSWORD] rejected", *tree.Run.Failure)

	// An empty reason stores NULL rather than an empty string.
	empty := f.startRun(t)
	require.NoError(t, f.svc.FailRun(ctx, empty.ID, nil, "   "))
	row, err := f.svc.Run(ctx, empty.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", row.Status)
	require.Nil(t, row.Failure)

	// A long reason is cut with the truncation suffix.
	long := f.startRun(t)
	require.NoError(t, f.svc.FailRun(ctx, long.ID, nil, strings.Repeat("x", MaxFailureBytes*2)))
	row, err = f.svc.Run(ctx, long.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Failure)
	require.Len(t, *row.Failure, MaxFailureBytes)
	require.True(t, strings.HasSuffix(*row.Failure, truncationSuffix))

	// Succeeded and cancelled runs never carry a reason.
	ok := f.startRun(t)
	require.NoError(t, f.svc.FinishRun(ctx, ok.ID, RunSucceeded))
	row, err = f.svc.Run(ctx, ok.ID)
	require.NoError(t, err)
	require.Nil(t, row.Failure)
	cancelled := f.startRun(t)
	require.NoError(t, f.svc.FinishRun(ctx, cancelled.ID, RunCancelled))
	row, err = f.svc.Run(ctx, cancelled.ID)
	require.NoError(t, err)
	require.Nil(t, row.Failure)
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

	// A terminal run past the age cap goes anywhere, with the next finish
	// of any run; a younger one and a running one stay.
	aged, err := f.svc.CreateRun(ctx, RunInput{Kind: "rotation", Actor: "tester"})
	require.NoError(t, err)
	require.NoError(t, f.svc.StartRun(ctx, aged.ID))
	require.NoError(t, f.svc.FinishRun(ctx, aged.ID, RunSucceeded))
	_, err = f.st.Pool.Exec(ctx, "UPDATE runs SET finished_at = $2 WHERE id = $1",
		aged.ID, time.Now().Add(-MaxRunAge-time.Hour))
	require.NoError(t, err)
	young, err := f.svc.CreateRun(ctx, RunInput{Kind: "rotation", Actor: "tester"})
	require.NoError(t, err)
	require.NoError(t, f.svc.StartRun(ctx, young.ID))
	require.NoError(t, f.svc.FinishRun(ctx, young.ID, RunSucceeded))
	_, err = f.svc.Run(ctx, aged.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = f.svc.Run(ctx, young.ID)
	require.NoError(t, err)
	runs, err = f.svc.ListRuns(ctx, f.environmentID)
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

// DeferredRoutes counts only TLS checkpoints skipped as deferred: a
// checkpoint skipped by the run's own conclusion carries no deferred
// snapshot and does not count.
func TestDeferredRoutes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	deferred := f.startRun(t)
	step, err := f.svc.EnsureStep(ctx, deferred.ID, nil, "tls:tls-web-public", "Issue TLS certificate")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, step.ID, StepWaiting))
	attempt, err := f.svc.StartAttempt(ctx, step.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Writer(attempt.ID, nil).Log(ctx, "warn", "TLS deferred", map[string]any{"tls": true, "phase": "deferred"}))
	require.NoError(t, f.svc.FinishAttempt(ctx, attempt.ID, AttemptSucceeded))
	require.NoError(t, f.svc.SetStepStatus(ctx, step.ID, StepSkipped))
	other, err := f.svc.EnsureStep(ctx, deferred.ID, nil, "tls:tls-web-admin", "Issue TLS certificate")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, other.ID, StepWaiting))
	require.NoError(t, f.svc.FinishRun(ctx, deferred.ID, RunSucceeded))

	cancelled := f.startRun(t)
	waiting, err := f.svc.EnsureStep(ctx, cancelled.ID, nil, "tls:tls-web-public", "Issue TLS certificate")
	require.NoError(t, err)
	require.NoError(t, f.svc.SetStepStatus(ctx, waiting.ID, StepWaiting))
	require.NoError(t, f.svc.FinishRun(ctx, cancelled.ID, RunCancelled))

	counts, err := f.svc.DeferredRoutes(ctx, []uuid.UUID{deferred.ID, cancelled.ID})
	require.NoError(t, err)
	require.Equal(t, map[uuid.UUID]int64{deferred.ID: 1}, counts)
	counts, err = f.svc.DeferredRoutes(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, counts)
}
