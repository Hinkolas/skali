package journal

import (
	"testing"

	"github.com/Hinkolas/skali/internal/lifecycle"
	"github.com/stretchr/testify/require"
)

func TestMachinesAreWellFormed(t *testing.T) {
	t.Parallel()
	require.NoError(t, lifecycle.Verify(Runs))
	require.NoError(t, lifecycle.Verify(Steps))
	require.NoError(t, lifecycle.Verify(Attempts))
}

func TestRunMachine(t *testing.T) {
	t.Parallel()
	require.Equal(t, RunPending, Runs.Initial())
	for status, terminal := range map[RunStatus]bool{
		RunPending:   false,
		RunRunning:   false,
		RunSucceeded: true,
		RunFailed:    true,
		RunCancelled: true,
	} {
		require.Equal(t, terminal, Runs.Terminal(status), status)
	}
	// A queued run may be cancelled without ever starting.
	require.True(t, Runs.Can(RunPending, RunCancelled))
	// A pending run cannot succeed or fail without running.
	require.False(t, Runs.Can(RunPending, RunSucceeded))
	require.False(t, Runs.Can(RunPending, RunFailed))
	require.False(t, Runs.Can(RunSucceeded, RunRunning))
}

func TestStepMachine(t *testing.T) {
	t.Parallel()
	require.Equal(t, StepPending, Steps.Initial())
	// A step must pass through running to succeed; a resolved dependency does
	// not skip execution.
	require.False(t, Steps.Can(StepWaiting, StepSucceeded))
	require.False(t, Steps.Can(StepPending, StepSucceeded))
	// A running step may discover a dependency and wait visibly.
	require.True(t, Steps.Can(StepRunning, StepWaiting))
	// A waiting step fails on policy timeout.
	require.True(t, Steps.Can(StepWaiting, StepFailed))
	// Unstarted work is skipped when the run terminates early.
	require.True(t, Steps.Can(StepPending, StepSkipped))
	require.True(t, Steps.Can(StepWaiting, StepSkipped))
	require.False(t, Steps.Can(StepRunning, StepSkipped))
	for _, status := range []StepStatus{StepSucceeded, StepFailed, StepSkipped, StepCancelled} {
		require.True(t, Steps.Terminal(status), status)
	}
}

func TestAttemptMachine(t *testing.T) {
	t.Parallel()
	// An attempt exists only once it has started.
	require.Equal(t, AttemptRunning, Attempts.Initial())
	for _, status := range []AttemptStatus{AttemptSucceeded, AttemptFailed, AttemptCancelled} {
		require.True(t, Attempts.Terminal(status), status)
		require.False(t, Attempts.Can(status, AttemptRunning))
	}
}
