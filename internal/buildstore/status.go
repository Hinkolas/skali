package buildstore

import "github.com/Hinkolas/skali/internal/lifecycle"

type Status string

const (
	// StatusQueued: a worker build awaiting a claim (R4 worker protocol).
	StatusQueued Status = "queued"
	// StatusClaimed: a worker holds the lease but has not reported the
	// build running yet (R4 worker protocol). An expired lease returns the
	// build to queued.
	StatusClaimed Status = "claimed"
	// StatusRunning: the build is executing. Local builds enter the machine
	// here: they are records of work the CLI already started, never queue
	// entries.
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Statuses is the build lifecycle machine. The entry state depends on the
// origin: worker builds start queued, local builds start running (the
// database check constraint enforces that a local build can never occupy a
// queue state).
var Statuses = lifecycle.Machine[Status]{
	States: []Status{
		StatusQueued, StatusClaimed, StatusRunning,
		StatusSucceeded, StatusFailed, StatusCancelled,
	},
	Transitions: map[Status][]Status{
		StatusQueued:  {StatusClaimed, StatusCancelled},
		StatusClaimed: {StatusRunning, StatusQueued, StatusCancelled},
		StatusRunning: {StatusSucceeded, StatusFailed, StatusCancelled},
	},
}
