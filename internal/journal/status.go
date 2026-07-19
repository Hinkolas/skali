// Package journal defines the execution-journal vocabulary: run, step, and
// attempt statuses and their transitions. Runs, steps, attempts, and log
// entries explain reconciliation and other operations; they never drive them.
// Deleting the whole journal must leave targets, recovery, and reconciliation
// behavior unchanged.
package journal

import "github.com/Hinkolas/skali/internal/lifecycle"

type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// Runs is the run status machine. A run queued behind an environment's single
// in-flight rollout stays pending until it starts or is cancelled.
var Runs = lifecycle.Machine[RunStatus]{
	States: []RunStatus{RunPending, RunRunning, RunSucceeded, RunFailed, RunCancelled},
	Transitions: map[RunStatus][]RunStatus{
		RunPending: {RunRunning, RunCancelled},
		RunRunning: {RunSucceeded, RunFailed, RunCancelled},
	},
}

type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepWaiting   StepStatus = "waiting"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
	StepCancelled StepStatus = "cancelled"
)

// Steps is the step status machine. Waiting is the visible dependency state:
// a step that cannot proceed names what it waits for instead of retrying
// opaquely, and a policy timeout fails it. Retries stay inside running; the
// step leaves running only when it succeeds, retry policy declares it failed,
// a dependency sends it back to waiting, or the run is cancelled. A run
// reaching a terminal status forces every non-terminal step terminal:
// running work is cancelled or failed, unstarted work is skipped.
var Steps = lifecycle.Machine[StepStatus]{
	States: []StepStatus{
		StepPending, StepWaiting, StepRunning,
		StepSucceeded, StepFailed, StepSkipped, StepCancelled,
	},
	Transitions: map[StepStatus][]StepStatus{
		StepPending: {StepWaiting, StepRunning, StepSkipped, StepCancelled},
		StepWaiting: {StepRunning, StepSkipped, StepFailed, StepCancelled},
		StepRunning: {StepWaiting, StepSucceeded, StepFailed, StepCancelled},
	},
}

type AttemptStatus string

const (
	AttemptRunning   AttemptStatus = "running"
	AttemptSucceeded AttemptStatus = "succeeded"
	AttemptFailed    AttemptStatus = "failed"
	AttemptCancelled AttemptStatus = "cancelled"
)

// Attempts is the attempt status machine. An attempt exists only once it has
// started, so running is initial, and a new attempt may start only after the
// previous one is terminal. On controller restart, an attempt whose executor
// no longer exists is failed with a restart diagnostic; the deterministic
// step key lets the controller reattach and continue the same run.
var Attempts = lifecycle.Machine[AttemptStatus]{
	States: []AttemptStatus{AttemptRunning, AttemptSucceeded, AttemptFailed, AttemptCancelled},
	Transitions: map[AttemptStatus][]AttemptStatus{
		AttemptRunning: {AttemptSucceeded, AttemptFailed, AttemptCancelled},
	},
}
